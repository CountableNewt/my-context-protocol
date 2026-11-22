package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/oauth"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func flag(key string) bool {
	return strings.EqualFold(os.Getenv(key), "true") || os.Getenv(key) == "1" || strings.EqualFold(os.Getenv(key), "yes")
}
func validateHostedMCPPath(path string, vercel bool) error {
	if vercel && path != "/mcp" {
		return errors.New("Vercel public ingress is fixed at /mcp; set SAAS_MCP_PATH=/mcp or update vercel.json before deploying a custom path")
	}
	return nil
}
func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	store := mcp.SQLStore{DB: db}
	origins := identity.FrontendOriginsFromEnvironment(os.Getenv)
	for _, part := range strings.Split(os.Getenv("MCP_ALLOWED_ORIGINS"), ",") {
		if strings.TrimSpace(part) != "" {
			origins = append(origins, strings.TrimRight(strings.TrimSpace(part), "/"))
		}
	}
	production := os.Getenv("APP_ENV") == "prod" || os.Getenv("APP_ENV") == "production" || os.Getenv("VERCEL_ENV") == "production"
	limit := 0
	if production || flag("RATE_LIMIT_MCP_ENABLED") {
		limit = 300
		if configured, e := strconv.Atoi(os.Getenv("MCP_RATE_LIMIT_PER_MINUTE")); e == nil && configured > 0 {
			limit = configured
		}
	}
	frontend := ""
	if len(origins) > 0 {
		frontend = origins[0]
	}
	mcpPath := os.Getenv("SAAS_MCP_PATH")
	if strings.TrimSpace(mcpPath) == "" {
		mcpPath = os.Getenv("MCP_PATH")
	}
	config := mcp.Config{Path: mcpPath, BaseDomain: os.Getenv("SAAS_MCP_BASE_DOMAIN"), RequireTenantHost: production || flag("REQUIRE_MCP_TENANT_HOST"), TrustForwardedHost: flag("MCP_TRUST_X_FORWARDED_HOST"), TrustVercelClientIP: os.Getenv("VERCEL") == "1", AllowedOrigins: origins, FrontendOrigin: frontend, OAuthEnabled: flag("MCP_OAUTH_ENABLED"), RateLimitPerMinute: limit}
	handler, err := mcp.New(config, store)
	if err != nil {
		log.Fatal(err)
	}
	config.Path = handler.Path()
	if err = validateHostedMCPPath(config.Path, os.Getenv("VERCEL") == "1"); err != nil {
		log.Fatal(err)
	}
	var oauthHandler *oauth.Service
	if config.OAuthEnabled {
		callback := os.Getenv("GITHUB_OAUTH_REDIRECT_URI")
		if callback == "" {
			callback = strings.TrimRight(os.Getenv("WEBHOOK_BASE_URL"), "/") + "/auth/github/callback"
		}
		oauthOrigins := []string{}
		for _, part := range strings.Split(os.Getenv("MCP_OAUTH_RESUME_BASE_URLS"), ",") {
			if strings.TrimSpace(part) != "" {
				oauthOrigins = append(oauthOrigins, strings.TrimSpace(part))
			}
		}
		if value := os.Getenv("MCP_OAUTH_API_ORIGIN"); value != "" {
			oauthOrigins = append(oauthOrigins, value)
		}
		identityHandler, e := identity.New(identity.Config{ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), RedirectURI: callback, EncryptionKey: os.Getenv("ENCRYPTION_KEY"), FrontendOrigins: identity.FrontendOriginsFromEnvironment(os.Getenv), OAuthReturnOrigins: oauthOrigins, AppEnvironment: os.Getenv("APP_ENV")}, identity.SQLStore{DB: db}, identity.NewGitHubHTTP())
		if e != nil {
			log.Fatal(e)
		}
		originForProject := func(p *mcp.Project) string {
			if p == nil {
				return ""
			}
			if p.CustomDomain != "" {
				return "https://" + p.CustomDomain
			}
			if p.Subdomain != "" && config.BaseDomain != "" {
				return "https://" + p.Subdomain + "." + config.BaseDomain
			}
			return ""
		}
		oauthHandler, e = oauth.New(oauth.Config{Enabled: true, AllowedOrigins: origins, MCPPath: config.Path, APIOrigin: os.Getenv("MCP_OAUTH_API_ORIGIN"), GitHubLogin: identityHandler.MCPLoginURL, Resolve: func(r *http.Request) (*oauth.Project, error) {
			p, e := handler.ResolveTenant(r)
			if e != nil || p == nil {
				return nil, e
			}
			return &oauth.Project{ID: p.ID, AccountID: p.AccountID, Name: p.Name, Origin: handler.PublicOrigin(r)}, nil
		}, ProjectByID: func(ctx context.Context, id string) (*oauth.Project, error) {
			p, e := store.ProjectByID(ctx, id)
			if e != nil || p == nil {
				return nil, e
			}
			return &oauth.Project{ID: p.ID, AccountID: p.AccountID, Name: p.Name, Origin: originForProject(p)}, nil
		}}, db, identity.SQLStore{DB: db})
		if e != nil {
			log.Fatal(e)
		}
	}
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oauthHandler != nil && oauth.Handles(r.URL.Path) {
			if allowed, e := handler.AllowIngress(r); e != nil {
				http.Error(w, "Rate limiting unavailable", 503)
				return
			} else if !allowed {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "Rate limited", 429)
				return
			}
			oauthHandler.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	server := &http.Server{Addr: ":" + port, Handler: public, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
