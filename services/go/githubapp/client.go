// Package githubapp implements bounded GitHub App and repository API operations.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
)

const maxResponse = 4 << 20

var ErrNotConfigured = &PublicError{501, "GitHub App is not configured", nil}
var ErrForbidden = errors.New("GitHub repository access denied")
var ErrInstallationScope = errors.New("repository is not included in the GitHub App installation")

type APIError struct {
	Status int
	Action string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub %s failed (HTTP %d)", e.Action, e.Status)
}

type Client struct {
	ClientID, ClientSecret, SetupCallback string
	Key                                   *rsa.PrivateKey
	HTTP                                  *http.Client
}

func LoadPrivateKey(raw, encoded string) (*rsa.PrivateKey, error) {
	if encoded != "" {
		compact := strings.Join(strings.Fields(encoded), "")
		compact = strings.NewReplacer("-", "+", "_", "/").Replace(compact)
		compact += strings.Repeat("=", (4-len(compact)%4)%4)
		data, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return nil, errors.New("invalid GitHub App private-key base64")
		}
		raw = string(data)
	}
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "\ufeff"))
	raw = strings.Trim(raw, "\"")
	raw = strings.ReplaceAll(raw, `\n`, "\n")
	raw = strings.ReplaceAll(raw, "\r", "")
	for _, kind := range []string{"RSA PRIVATE KEY", "PRIVATE KEY"} {
		begin, end := "-----BEGIN "+kind+"-----", "-----END "+kind+"-----"
		if i := strings.Index(raw, begin); i >= 0 {
			j := strings.Index(raw, end)
			if j < i {
				return nil, errors.New("invalid GitHub App private key")
			}
			body := strings.Join(strings.Fields(raw[i+len(begin):j]), "")
			var wrapped strings.Builder
			for len(body) > 64 {
				wrapped.WriteString(body[:64] + "\n")
				body = body[64:]
			}
			wrapped.WriteString(body)
			raw = begin + "\n" + wrapped.String() + "\n" + end + "\n"
			break
		}
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("invalid GitHub App private-key PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		var err error
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid GitHub App RSA key")
		}
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App requires an RSA key")
		}
	default:
		return nil, errors.New("GitHub App requires an RSA private key")
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("GitHub App RSA key must be at least 2048 bits")
	}
	return key, key.Validate()
}
func (c *Client) AppJWT(now time.Time) (string, error) {
	if c.ClientID == "" || c.Key == nil {
		return "", ErrNotConfigured
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iss": c.ClientID, "iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(600 * time.Second).Unix()})
	payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
func (c *Client) request(ctx context.Context, method, target, token string, body any) ([]byte, http.Header, int, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || (parsed.Host != "api.github.com" && parsed.Host != "github.com") || parsed.User != nil {
		return nil, nil, 0, errors.New("untrusted GitHub API URL")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, nil, 0, err
		}
		if len(data) > 1<<20 {
			return nil, nil, 0, errors.New("GitHub request exceeds limit")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if parsed.Host == "github.com" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "MyContextProtocol/1")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, nil, 0, err
	}
	if len(data) > maxResponse {
		return nil, nil, 0, errors.New("GitHub response exceeds limit")
	}
	return data, response.Header, response.StatusCode, nil
}
func (c *Client) api(ctx context.Context, method, target, token, action string, body, result any, allowed ...int) error {
	data, _, status, err := c.request(ctx, method, target, token, body)
	if err != nil {
		return err
	}
	ok := false
	for _, n := range allowed {
		ok = ok || status == n
	}
	if !ok {
		return &APIError{status, action}
	}
	if result != nil {
		if err = json.Unmarshal(data, result); err != nil {
			return errors.New("invalid GitHub " + action + " response")
		}
	}
	return nil
}
func idPath(id string) (string, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return "", errors.New("invalid GitHub installation id")
	}
	return strconv.FormatInt(n, 10), nil
}

type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"account"`
	Permissions map[string]string `json:"permissions"`
}

func (c *Client) Installation(ctx context.Context, id string) (Installation, error) {
	var result Installation
	id, err := idPath(id)
	if err != nil {
		return result, err
	}
	jwt, err := c.AppJWT(time.Now())
	if err != nil {
		return result, err
	}
	err = c.api(ctx, "GET", "https://api.github.com/app/installations/"+id, jwt, "installation lookup", nil, &result, 200)
	if err == nil && (result.ID == 0 || result.Account.ID == 0) {
		err = errors.New("incomplete GitHub installation response")
	}
	return result, err
}

// InstallationToken preserves syncservice's stable callback signature. An App
// installation is always exchanged; OAuth never silently substitutes for it.
func (c *Client) InstallationToken(ctx context.Context, id, oauth string) (string, error) {
	id, err := idPath(id)
	if err != nil {
		return "", err
	}
	jwt, err := c.AppJWT(time.Now())
	if err != nil {
		return "", err
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err = c.api(ctx, "POST", "https://api.github.com/app/installations/"+id+"/access_tokens", jwt, "installation token exchange", map[string]any{}, &result, 201)
	if err != nil {
		return "", err
	}
	if result.Token == "" || (!result.ExpiresAt.IsZero() && !result.ExpiresAt.After(time.Now())) {
		return "", errors.New("missing or expired GitHub installation token")
	}
	return result.Token, nil
}
func repoAPI(owner, repo string) (string, error) {
	endpoint, err := reposync.ArchiveURL(owner, repo, "main")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(endpoint, "/tarball/main"), nil
}

type Repository struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions map[string]bool `json:"permissions"`
}

func (c *Client) VerifyRepository(ctx context.Context, owner, repo, token, fallback string) (Repository, error) {
	var result Repository
	base, err := repoAPI(owner, repo)
	if err != nil {
		return result, err
	}
	err = c.api(ctx, "GET", base, token, "repository lookup", nil, &result, 200)
	if err == nil {
		return result, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 403 || apiErr.Status == 404) && fallback != "" {
		var userResult Repository
		if c.api(ctx, "GET", base, fallback, "repository lookup", nil, &userResult, 200) == nil {
			return result, ErrInstallationScope
		}
	}
	return result, err
}
func (c *Client) ListRepositories(ctx context.Context, token string) ([]Repository, error) {
	result := []Repository{}
	next := "https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member"
	for page := 0; next != "" && page < 10; page++ {
		data, headers, status, err := c.request(ctx, "GET", next, token, nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, &APIError{status, "repository listing"}
		}
		var rows []Repository
		if json.Unmarshal(data, &rows) != nil {
			return nil, errors.New("invalid repository list")
		}
		if len(rows) > 100 {
			return nil, errors.New("repository page exceeds limit")
		}
		result = append(result, rows...)
		next = ""
		for _, link := range strings.Split(headers.Get("Link"), ",") {
			parts := strings.Split(link, ";")
			if len(parts) == 2 && strings.TrimSpace(parts[1]) == `rel="next"` {
				candidate := strings.Trim(strings.TrimSpace(parts[0]), "<>")
				u, err := url.Parse(candidate)
				if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.Path != "/user/repos" || u.User != nil {
					return nil, errors.New("untrusted repository pagination URL")
				}
				next = candidate
			}
		}
	}
	return result, nil
}
func (c *Client) CreateWebhook(ctx context.Context, owner, repo, token, origin string) (string, string, error) {
	base, err := repoAPI(owner, repo)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("webhook origin must use HTTPS")
	}
	secret, err := security.NewOpaqueToken()
	if err != nil {
		return "", "", err
	}
	var result struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"name": "web", "active": true, "events": []string{"push"}, "config": map[string]any{"url": strings.TrimRight(origin, "/") + "/webhooks/github", "content_type": "json", "secret": secret, "insecure_ssl": "0"}}
	err = c.api(ctx, "POST", base+"/hooks", token, "webhook creation", body, &result, 201)
	if err != nil {
		return "", "", err
	}
	if result.ID <= 0 {
		return "", "", errors.New("missing GitHub webhook id")
	}
	return strconv.FormatInt(result.ID, 10), secret, nil
}
func (c *Client) DeleteWebhook(ctx context.Context, owner, repo, id, token string) error {
	base, err := repoAPI(owner, repo)
	if err != nil {
		return err
	}
	if _, err = idPath(id); err != nil {
		return err
	}
	return c.api(ctx, "DELETE", base+"/hooks/"+url.PathEscape(id), token, "webhook deletion", nil, nil, 204, 404)
}
func (c *Client) ExchangeInstallOAuth(ctx context.Context, code string) (string, int64, error) {
	if c.ClientID == "" || c.ClientSecret == "" || c.SetupCallback == "" {
		return "", 0, ErrNotConfigured
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	err := c.api(ctx, "POST", "https://github.com/login/oauth/access_token", "", "installation OAuth exchange", map[string]any{"client_id": c.ClientID, "client_secret": c.ClientSecret, "code": code, "redirect_uri": c.SetupCallback}, &result, 200)
	if err != nil {
		return "", 0, err
	}
	if result.AccessToken == "" {
		return "", 0, errors.New("missing GitHub user token")
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err = c.api(ctx, "GET", "https://api.github.com/user", result.AccessToken, "user lookup", nil, &user, 200); err != nil {
		return "", 0, err
	}
	if user.ID <= 0 {
		return "", 0, errors.New("missing GitHub user identity")
	}
	return result.AccessToken, user.ID, nil
}

// UserInstallation resolves OAuth-on-install callbacks that provide only code
// and state. The token has already been verified through /user; candidate
// personal installations must belong to that same GitHub identity. The caller
// independently validates the selected installation with the App JWT.
func (c *Client) UserInstallation(ctx context.Context, token string, githubID int64) (string, error) {
	if token == "" || githubID <= 0 {
		return "", ErrForbidden
	}
	found := map[int64]bool{}
	for page := 1; page <= 10; page++ {
		var result struct {
			Installations []Installation `json:"installations"`
			Total         int            `json:"total_count"`
		}
		target := "https://api.github.com/user/installations?per_page=100&page=" + strconv.Itoa(page)
		if err := c.api(ctx, "GET", target, token, "user installation lookup", nil, &result, 200); err != nil {
			return "", err
		}
		if len(result.Installations) > 100 || result.Total > 1000 {
			return "", &PublicError{409, "GitHub installation list exceeds supported bounds", nil}
		}
		for _, candidate := range result.Installations {
			if candidate.ID > 0 && candidate.Account.Type == "User" && candidate.Account.ID == githubID {
				found[candidate.ID] = true
			}
		}
		if len(result.Installations) < 100 {
			break
		}
		if page == 10 {
			return "", &PublicError{409, "GitHub installation list is incomplete", nil}
		}
	}
	if len(found) != 1 {
		return "", &PublicError{409, "Could not identify one personal GitHub App installation for the signed-in user", nil}
	}
	for id := range found {
		return strconv.FormatInt(id, 10), nil
	}
	return "", ErrForbidden
}

// VerifyAppWebhook verifies existing App registration; it never changes GitHub
// permissions, subscriptions or webhook configuration. Both shipped hook routes
// support signed App push and lifecycle deliveries.
func (c *Client) VerifyAppWebhook(ctx context.Context, origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return &PublicError{503, "GitHub webhook origin is not configured", nil}
	}
	jwt, err := c.AppJWT(time.Now())
	if err != nil {
		return err
	}
	var registration struct {
		Events []string `json:"events"`
	}
	if err = c.api(ctx, "GET", "https://api.github.com/app", jwt, "App event lookup", nil, &registration, 200); err != nil {
		return err
	}
	push := false
	for _, event := range registration.Events {
		push = push || event == "push"
	}
	if !push {
		return &PublicError{503, "GitHub App must already subscribe to push webhook events", nil}
	}
	var hook struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		InsecureSSL string `json:"insecure_ssl"`
	}
	if err = c.api(ctx, "GET", "https://api.github.com/app/hook/config", jwt, "App webhook lookup", nil, &hook, 200); err != nil {
		return err
	}
	base := strings.TrimRight(origin, "/")
	if hook.URL != base+"/webhooks/github" && hook.URL != base+"/webhooks/github-app" || hook.ContentType != "json" || hook.InsecureSSL != "0" {
		return &PublicError{503, "GitHub App signed JSON webhook must already target this service with TLS verification", nil}
	}
	return nil
}
