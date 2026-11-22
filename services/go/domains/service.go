// Package domains provisions verified tenant domains in the single Vercel
// project. Application ownership and Vercel routing readiness are separate checks.
package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
}
type Resolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}
type Config struct {
	Token, ProjectID, TeamID                                                string
	IntegrationClientID, IntegrationClientSecret, IntegrationInstallationID string
	ReservedDomains                                                         []string
}
type Service struct {
	db          Database
	config      Config
	resolver    Resolver
	http        *http.Client
	api         string
	tokenMu     sync.Mutex
	cachedToken string
	tokenExpiry time.Time
	now         func() time.Time
}
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string   { return e.Message }
func (e *Error) HTTPStatus() int { return e.Status }
func New(db Database, c Config) (*Service, error) {
	integration := c.IntegrationClientID != "" || c.IntegrationClientSecret != "" || c.IntegrationInstallationID != ""
	if db == nil || c.ProjectID == "" || c.TeamID == "" || (c.Token == "" && !integration) || (c.Token != "" && integration) || (c.Token != "" && !validBearer(c.Token)) || (integration && (c.IntegrationClientID == "" || c.IntegrationClientSecret == "" || !strings.HasPrefix(c.IntegrationInstallationID, "icfg_") || strings.ContainsAny(c.IntegrationClientID+c.IntegrationClientSecret+c.IntegrationInstallationID, "\r\n"))) {
		return nil, errors.New("Vercel domain database, project, team and exactly one complete credential mode are required")
	}
	return &Service{db: db, config: c, resolver: net.DefaultResolver, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, api: "https://api.vercel.com", now: time.Now}, nil
}
func (s *Service) hostname(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.HasSuffix(host, ".vercel.app") {
		return "", &Error{400, "Invalid custom domain hostname"}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", &Error{400, "Invalid custom domain hostname"}
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", &Error{400, "Invalid custom domain hostname"}
			}
		}
	}
	for _, reserved := range s.config.ReservedDomains {
		reserved = strings.ToLower(strings.TrimSuffix(reserved, "."))
		if reserved != "" && (host == reserved || strings.HasSuffix(host, "."+reserved)) {
			return "", &Error{400, "Platform domains cannot be assigned as custom domains"}
		}
	}
	return host, nil
}

type project struct {
	id, host, token string
	verified        bool
}

func (s *Service) load(ctx context.Context, id string) (*project, error) {
	r, e := s.db.Execute(ctx, turso.Statement{SQL: "SELECT id,custom_domain,custom_domain_verification_token,custom_domain_verified_at FROM projects WHERE id=?", Args: []turso.Value{turso.Text(id)}, WantRows: true})
	if e != nil {
		return nil, e
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 4 {
		return nil, &Error{404, "Project not found"}
	}
	row := r.Rows[0]
	str := func(i int) string { v, _ := row[i].Value.(string); return v }
	return &project{str(0), str(1), str(2), row[3].Type != "null"}, nil
}

type challenge struct{ Type, Domain, Value string }
type domain struct {
	Name         string      `json:"name"`
	ProjectID    string      `json:"projectId"`
	Verified     bool        `json:"verified"`
	Verification []challenge `json:"verification"`
}
type routing struct {
	Misconfigured *bool `json:"misconfigured"`
	CNAME         []struct {
		Rank  int    `json:"rank"`
		Value string `json:"value"`
	} `json:"recommendedCNAME"`
	IPv4 []struct {
		Rank  int      `json:"rank"`
		Value []string `json:"value"`
	} `json:"recommendedIPv4"`
}

func (s *Service) call(ctx context.Context, method, path string, body any, out any) error {
	var data []byte
	var e error
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	u := s.api + path
	separator := "?"
	if strings.Contains(u, "?") {
		separator = "&"
	}
	u += separator + "teamId=" + url.QueryEscape(s.config.TeamID)
	req, e := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(data))
	if e != nil {
		return e
	}
	token, e := s.accessToken(ctx)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, e := s.http.Do(req)
	if e != nil {
		return &Error{503, "Vercel domain API unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized && s.config.Token == "" {
		s.tokenMu.Lock()
		if s.cachedToken == token {
			s.cachedToken = ""
			s.tokenExpiry = time.Time{}
		}
		s.tokenMu.Unlock()
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		status := 503
		message := "Vercel domain provisioning unavailable"
		if res.StatusCode == 404 {
			status = 404
			message = "Vercel project domain not found"
		}
		if res.StatusCode == 409 {
			status = 409
			message = "Domain is assigned to another Vercel project"
		}
		if res.StatusCode == 429 {
			status = 429
			message = "Vercel domain quota or rate limit reached; retry later"
		}
		return &Error{status, message}
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return &Error{503, "Invalid Vercel domain response"}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return &Error{503, "Invalid Vercel domain response"}
	}
	return nil
}
func (s *Service) path(host string) string {
	return "/v9/projects/" + url.PathEscape(s.config.ProjectID) + "/domains/" + url.PathEscape(host)
}
func (s *Service) provider(ctx context.Context, host string) (*domain, *routing, error) {
	var d domain
	if e := s.call(ctx, "GET", s.path(host), nil, &d); e != nil {
		return nil, nil, e
	}
	if d.Name != host || d.ProjectID != s.config.ProjectID {
		return nil, nil, &Error{503, "Vercel returned an unexpected domain association"}
	}
	var c routing
	if e := s.call(ctx, "GET", "/v6/domains/"+url.PathEscape(host)+"/config?projectIdOrName="+url.QueryEscape(s.config.ProjectID), nil, &c); e != nil {
		return nil, nil, e
	}
	return &d, &c, nil
}
func (s *Service) ensure(ctx context.Context, host string) error {
	var d domain
	e := s.call(ctx, "GET", s.path(host), nil, &d)
	if e == nil {
		if d.Name != host || d.ProjectID != s.config.ProjectID {
			return &Error{409, "Domain association is not owned by this Vercel project"}
		}
		return nil
	}
	var status *Error
	if !errors.As(e, &status) || status.Status != 404 {
		return e
	}
	if e = s.call(ctx, "POST", "/v10/projects/"+url.PathEscape(s.config.ProjectID)+"/domains", map[string]string{"name": host}, &d); e != nil {
		return e
	}
	if d.Name != host || d.ProjectID != s.config.ProjectID {
		return &Error{503, "Vercel returned an unexpected domain association"}
	}
	return nil
}
func (s *Service) Get(ctx context.Context, id string) (map[string]any, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return nil, e
	}
	if p.host == "" {
		return map[string]any{"hostname": nil, "verified": false, "certificate_status": "not_configured", "platform_dns_records": []any{}}, nil
	}
	current, c, e := s.provider(ctx, p.host)
	if e != nil {
		return nil, e
	}
	return response(p, current, c), nil
}
func (s *Service) Set(ctx context.Context, id, raw string) (map[string]any, error) {
	host, e := s.hostname(raw)
	if e != nil {
		return nil, e
	}
	if _, e = s.load(ctx, id); e != nil {
		return nil, e
	}
	other, e := s.db.Execute(ctx, turso.Statement{SQL: "SELECT id FROM projects WHERE custom_domain=? AND id<>?", Args: []turso.Value{turso.Text(host), turso.Text(id)}, WantRows: true})
	if e != nil {
		return nil, e
	}
	if len(other.Rows) > 0 {
		return nil, &Error{409, "Domain is registered to another project"}
	}
	if e = s.ensure(ctx, host); e != nil {
		return nil, e
	}
	token, e := security.NewOpaqueToken()
	if e != nil {
		return nil, e
	}
	r, e := s.db.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET custom_domain=?,custom_domain_verification_token=?,custom_domain_verified_at=NULL WHERE id=?", Args: []turso.Value{turso.Text(host), turso.Text(token), turso.Text(id)}})
	if e != nil {
		return nil, &Error{409, "Could not reserve domain for this project"}
	}
	if r.AffectedRowCount != 1 {
		return nil, &Error{404, "Project not found"}
	}
	return s.Get(ctx, id)
}
func (s *Service) txt(ctx context.Context, name, token string) bool {
	records, e := s.resolver.LookupTXT(ctx, name)
	if e != nil {
		return false
	}
	for _, value := range records {
		if value == token {
			return true
		}
	}
	return false
}
func (s *Service) Verify(ctx context.Context, id string) (map[string]any, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return nil, e
	}
	if p.host == "" {
		return nil, &Error{400, "Set a custom domain first"}
	}
	if !p.verified && (p.token == "" || !s.txt(ctx, "_mcp-verify."+p.host, p.token) && !s.txt(ctx, p.host, p.token)) {
		return nil, &Error{400, "Add the project ownership TXT record before verifying"}
	}
	if e = s.ensure(ctx, p.host); e != nil {
		return nil, e
	}
	var d domain
	if e = s.call(ctx, "POST", s.path(p.host)+"/verify", nil, &d); e != nil {
		return nil, e
	}
	current, c, e := s.provider(ctx, p.host)
	if e != nil {
		return nil, e
	}
	if !current.Verified || c.Misconfigured == nil || *c.Misconfigured {
		return response(p, current, c), &Error{400, "Vercel is waiting for ownership and routing DNS records"}
	}
	r, e := s.db.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET custom_domain_verified_at=COALESCE(custom_domain_verified_at,?),custom_domain_verification_token=NULL WHERE id=? AND custom_domain=? AND custom_domain_verification_token IS ?", Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Text(id), turso.Text(p.host), nullable(p.token)}})
	if e != nil {
		return nil, e
	}
	if r.AffectedRowCount != 1 {
		return nil, &Error{409, "Domain changed while verification was in progress"}
	}
	p.verified = true
	p.token = ""
	return response(p, current, c), nil
}
func nullable(s string) turso.Value {
	if s == "" {
		return turso.Null()
	}
	return turso.Text(s)
}
func response(p *project, d *domain, c *routing) map[string]any {
	ready := d.Verified && c.Misconfigured != nil && !*c.Misconfigured
	status := "pending"
	message := "Complete the required DNS records; Vercel manages TLS at its edge."
	if ready {
		status = "unknown"
		message = "Vercel ownership and routing checks pass. TLS issuance is managed by Vercel and has not been independently checked."
	}
	records := []map[string]any{}
	for _, v := range d.Verification {
		records = append(records, map[string]any{"type": v.Type, "name": v.Domain, "value": v.Value, "status": "pending", "purpose": "OWNERSHIP_VERIFICATION"})
	}
	for _, v := range c.CNAME {
		if v.Rank == 1 {
			propagated := "pending"
			if ready {
				propagated = "DNS_RECORD_STATUS_PROPAGATED"
			}
			records = append(records, map[string]any{"type": "CNAME", "name": p.host, "value": v.Value, "status": propagated, "purpose": "DNS_RECORD_PURPOSE_TRAFFIC_ROUTE"})
		}
	}
	if len(c.CNAME) == 0 {
		for _, v := range c.IPv4 {
			if v.Rank == 1 {
				for _, ip := range v.Value {
					propagated := "pending"
					if ready {
						propagated = "DNS_RECORD_STATUS_PROPAGATED"
					}
					records = append(records, map[string]any{"type": "A", "name": p.host, "value": ip, "status": propagated, "purpose": "DNS_RECORD_PURPOSE_TRAFFIC_ROUTE"})
				}
			}
		}
	}
	var token, name any
	if p.token != "" {
		token = p.token
		name = "_mcp-verify." + p.host
	}
	return map[string]any{"hostname": p.host, "verified": p.verified && ready, "verification_token": token, "verification_record_name": name, "ownership_verification_record_name": name, "ownership_verification_record_value": token, "instructions": fmt.Sprintf("%s", message), "platform_dns_records": records, "certificate_status": status, "certificate_message": message}
}
