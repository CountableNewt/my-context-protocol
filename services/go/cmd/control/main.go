package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atplatform"
	"github.com/CountableNewt/my-context-protocol/services/go/control"
	"github.com/CountableNewt/my-context-protocol/services/go/domains"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/providerhub"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpcauth"
)

type syncError struct {
	status  int
	message string
}

func (e *syncError) Error() string   { return e.message }
func (e *syncError) HTTPStatus() int { return e.status }
func enabled(raw string) bool {
	return raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
}
func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	cipher, err := security.NewTokenCipher(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	origins := identity.FrontendOriginsFromEnvironment(os.Getenv)
	cfg := control.Config{CronSecret: os.Getenv("CRON_SECRET"), FrontendOrigins: origins, MCPBaseDomain: os.Getenv("SAAS_MCP_BASE_DOMAIN"), MCPPath: os.Getenv("SAAS_MCP_PATH"), MCPOAuthEnabled: enabled(os.Getenv("MCP_OAUTH_ENABLED"))}
	verifier, err := xrpcauth.FromEnvironment(db, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if verifier != nil {
		cfg.XRPCVerifier = verifier
	}
	platform, err := atplatform.New(db, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	var appDB control.DB = db
	if platform != nil {
		appDB = platform.DB
		cfg.Records = platform.Records
		cfg.CanonicalMaintenance = true
		cfg.ExtraMaintenance = func(ctx context.Context) (map[string]any, error) {
			return platform.Records.Maintenance(ctx, 2, 25, time.Now())
		}
	}
	cfg.MaintenanceRows, _ = strconv.Atoi(os.Getenv("MAINTENANCE_BATCH_ROWS"))
	cfg.MaintenanceRollupRows, _ = strconv.Atoi(os.Getenv("MAINTENANCE_ROLLUP_ROWS"))
	cfg.SyncLimit, _ = strconv.Atoi(os.Getenv("SYNC_RATE_LIMIT_MAX"))
	cfg.SyncWindowSeconds, _ = strconv.ParseInt(os.Getenv("SYNC_RATE_LIMIT_WINDOW_SECONDS"), 10, 64)
	catalog, err := mcp.New(mcp.Config{BaseDomain: cfg.MCPBaseDomain, Path: cfg.MCPPath, OAuthEnabled: cfg.MCPOAuthEnabled}, mcp.SQLStore{DB: appDB})
	if err != nil {
		log.Fatal(err)
	}
	cfg.Catalog = catalog
	if token := os.Getenv("VERCEL_TOKEN"); token != "" || os.Getenv("VERCEL_INTEGRATION_CLIENT_ID") != "" || os.Getenv("VERCEL_INTEGRATION_CLIENT_SECRET") != "" || os.Getenv("VERCEL_INTEGRATION_INSTALLATION_ID") != "" {
		reserved := []string{cfg.MCPBaseDomain}
		for _, origin := range origins {
			if u, e := url.Parse(origin); e == nil {
				reserved = append(reserved, u.Hostname())
			}
		}
		provider, e := domains.New(appDB, domains.Config{Token: token, IntegrationClientID: os.Getenv("VERCEL_INTEGRATION_CLIENT_ID"), IntegrationClientSecret: os.Getenv("VERCEL_INTEGRATION_CLIENT_SECRET"), IntegrationInstallationID: os.Getenv("VERCEL_INTEGRATION_INSTALLATION_ID"), ProjectID: os.Getenv("VERCEL_PROJECT_ID"), TeamID: os.Getenv("VERCEL_TEAM_ID"), ReservedDomains: reserved})
		if e != nil {
			log.Fatal(e)
		}
		cfg.Domains = provider
	}
	client := &githubapp.Client{ClientID: os.Getenv("GITHUB_APP_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_APP_CLIENT_SECRET"), SetupCallback: os.Getenv("GITHUB_APP_SETUP_CALLBACK_URL")}
	raw, encoded := os.Getenv("GITHUB_APP_PRIVATE_KEY"), os.Getenv("GITHUB_APP_PRIVATE_KEY_BASE64")
	if raw != "" || encoded != "" {
		client.Key, err = githubapp.LoadPrivateKey(raw, encoded)
		if err != nil {
			log.Fatal(err)
		}
	}
	github, err := githubapp.New(githubapp.Config{DB: appDB, Sessions: identity.SQLStore{DB: db}, Client: client, Cipher: cipher, Slug: os.Getenv("GITHUB_APP_SLUG"), WebhookOrigin: os.Getenv("WEBHOOK_BASE_URL"), AppWebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET"), FrontendOrigins: origins})
	if err != nil {
		log.Fatal(err)
	}
	cfg.ConnectRepository = github.ConnectRepository
	cfg.Writeback = github.Writeback
	var sync *syncservice.Service
	var hub *providerhub.Hub
	syncConfig := syncservice.Config{Store: syncservice.SQLStore{DB: appDB}, DecryptToken: cipher.Decrypt, InstallationToken: github.InstallationToken, WorkTimeout: 70 * time.Second}
	if platform != nil {
		hub, err = platform.Repositories(github.ConnectRepository, github.Writeback, func(ctx context.Context, pid string) (map[string]any, error) {
			if sync == nil {
				return nil, errors.New("sync is not configured")
			}
			return sync.SyncProject(ctx, pid)
		})
		if err != nil {
			log.Fatal(err)
		}
		cfg.ConnectRepository = hub.ConnectRepository
		cfg.Writeback = hub.Writeback
		cfg.ListTangledRepositories = hub.ListRepositories
		cfg.ResolveOwnerHandle = platform.Accounts.ResolveHandle
		cfg.ExtraMaintenance = func(ctx context.Context) (map[string]any, error) {
			records, err := platform.Records.Maintenance(ctx, 1, 25, time.Now())
			if err != nil {
				return nil, err
			}
			if platform.Tangled == nil {
				return map[string]any{"records": records}, nil
			}
			repositories, err := hub.Reconcile(ctx, 2)
			if err != nil {
				return nil, err
			}
			return map[string]any{"records": records, "repositories": repositories}, nil
		}
		syncConfig.Providers = map[string]syncservice.RepositoryProvider{"tangled": hub.Provider()}
		syncConfig.ProjectContext = platform.ScopeProject
	}
	sync, err = syncservice.New(syncConfig)
	if err != nil {
		log.Fatal(err)
	}
	// Native Go services cannot consume Vercel service bindings. Sharing the sync
	// library executes durable jobs in this request while preserving branch-local DB isolation.
	if platform != nil {
		cfg.SyncContinuation = (syncservice.SQLStore{DB: appDB}).PublicationPending
	}
	cfg.Sync = func(ctx context.Context, pid string) (map[string]any, error) {
		result, e := sync.SyncProject(ctx, pid)
		switch {
		case errors.Is(e, syncservice.ErrPending):
			return result, nil
		case errors.Is(e, syncservice.ErrBusy):
			return result, &syncError{409, "A sync is already running"}
		case errors.Is(e, syncservice.ErrNotFound):
			return result, &syncError{404, "No repository is connected"}
		case errors.Is(e, syncservice.ErrProviderUnsupported):
			return result, &syncError{501, "Repository provider is not configured"}
		case errors.Is(e, syncservice.ErrInvalid):
			return result, &syncError{422, "Repository validation failed; review the release validation report"}
		case errors.Is(e, syncservice.ErrUnsupported):
			return result, &syncError{409, "Repository metadata is unsupported; the current release remains active"}
		}
		return result, e
	}
	handler, err := control.New(appDB, identity.SQLStore{DB: db}, cfg)
	if err != nil {
		log.Fatal(err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
