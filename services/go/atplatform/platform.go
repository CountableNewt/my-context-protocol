// Package atplatform wires the record and repository adapters into hosted services.
package atplatform

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/providerhub"
	"github.com/CountableNewt/my-context-protocol/services/go/recordservice"
	"github.com/CountableNewt/my-context-protocol/services/go/sourcehydrate"
	"github.com/CountableNewt/my-context-protocol/services/go/tangled"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util/ssrf"
)

type Database interface{ ataccount.FullDatabase }
type Platform struct {
	githubSource  revisionSource
	recordSession func(context.Context, string, func(context.Context, atproto.Records) error) error
	Raw           Database
	DB            *recordservice.SQLProjection
	Records       *recordservice.Service
	Accounts      *ataccount.Manager
	Tangled       *tangled.Client
	GitHub        *githubapp.Client
}

func New(db Database, getenv func(string) string) (*Platform, error) {
	manager, _, err := ataccount.FromEnvironment(db, getenv)
	if err != nil {
		return nil, err
	}
	if manager == nil {
		if getenv("TANGLED_ENABLED") == "1" || strings.EqualFold(getenv("TANGLED_ENABLED"), "true") {
			return nil, errors.New("Tangled requires ATProto authorization to be enabled")
		}
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("ATProto platform database is required")
	}
	records := &recordservice.Service{DB: db, Accounts: manager, WorkLimit: 100}
	p := &Platform{Raw: db, Accounts: manager, Records: records, DB: &recordservice.SQLProjection{Raw: db, Records: records}}
	p.GitHub = &githubapp.Client{ClientID: getenv("GITHUB_APP_CLIENT_ID"), ClientSecret: getenv("GITHUB_APP_CLIENT_SECRET"), SetupCallback: getenv("GITHUB_APP_SETUP_CALLBACK_URL")}
	if raw, encoded := getenv("GITHUB_APP_PRIVATE_KEY"), getenv("GITHUB_APP_PRIVATE_KEY_BASE64"); raw != "" || encoded != "" {
		p.GitHub.Key, err = githubapp.LoadPrivateKey(raw, encoded)
		if err != nil {
			return nil, err
		}
	}
	p.Records.Hydrate = (sourcehydrate.Hydrator{DB: db, Fetch: p.FetchRevision, MaxSkills: 25}).Hydrate
	if getenv("TANGLED_ENABLED") == "1" || strings.EqualFold(getenv("TANGLED_ENABLED"), "true") {
		origin, did, mirror := getenv("TANGLED_API_URL"), getenv("TANGLED_SERVICE_DID"), getenv("TANGLED_ARCHIVE_MIRROR_URL")
		if origin == "" || did == "" || mirror == "" {
			return nil, errors.New("Tangled API URL, service DID and archive mirror must be explicitly configured")
		}
		if err = validateServiceOrigin(origin); err != nil {
			return nil, err
		}
		if err = validateServiceOrigin(mirror); err != nil {
			return nil, err
		}
		parts := strings.Split(did, "#")
		if len(parts) > 2 || len(parts) == 2 && (parts[1] == "" || strings.ContainsAny(parts[1], "/? \t\r\n")) {
			return nil, errors.New("invalid Tangled service identity")
		}
		if _, err = syntax.ParseDID(parts[0]); err != nil {
			return nil, errors.New("invalid Tangled service identity")
		}
		p.Tangled = &tangled.Client{ReadService: &tangled.ReadService{Origin: origin, DID: did, ArchiveMirrorOrigin: mirror}, Tokens: manager}
	}
	return p, nil
}

func validateServiceOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid Tangled public service origin")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || host == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" {
		return errors.New("Tangled service requires a public HTTPS origin")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ssrf.IsPublicIPAddress(ip) {
			return errors.New("Tangled service origin is not public")
		}
	} else if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return errors.New("Tangled service origin is not public")
	}
	return nil
}

func (p *Platform) withRecords(ctx context.Context, account string, apply func(context.Context, atproto.Records) error) error {
	if p.recordSession != nil {
		return p.recordSession(ctx, account, apply)
	}
	return p.Accounts.WithRecords(ctx, account, apply)
}

func (p *Platform) Repositories(connect func(context.Context, string, string, map[string]json.RawMessage) (map[string]any, error), writeback func(context.Context, string, string, string, string) (map[string]any, error), sync func(context.Context, string) (map[string]any, error)) (*providerhub.Hub, error) {
	var client providerhub.TangledAPI
	if p.Tangled != nil {
		client = *p.Tangled
	}
	return providerhub.New(providerhub.Config{DB: p.DB, Tangled: client, GitHubConnect: connect, GitHubWriteback: writeback, Sync: sync, Identity: func(ctx context.Context, account string) (string, error) {
		linked, err := p.Accounts.Identities.ATProtoIdentity(ctx, account)
		if err != nil {
			return "", err
		}
		if linked == nil {
			return "", ataccount.ErrReconnect
		}
		return linked.DID, nil
	}, Attestor: func(ctx context.Context, account string) (tangled.Attestor, error) {
		return &attestor{platform: p, account: account}, nil
	}})
}

func (p *Platform) ScopeProject(ctx context.Context, project string) (context.Context, error) {
	r, err := p.Raw.Execute(ctx, turso.Statement{SQL: `SELECT account_id FROM projects WHERE id=?`, Args: []turso.Value{turso.Text(project)}, WantRows: true})
	if err != nil {
		return ctx, err
	}
	if r == nil || len(r.Rows) != 1 {
		return ctx, errors.New("project not found")
	}
	account, ok := r.Rows[0][0].Value.(string)
	if !ok {
		return ctx, errors.New("invalid project owner")
	}
	return p.Records.Scope(ctx, account, project)
}
