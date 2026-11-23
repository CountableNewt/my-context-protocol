package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/oauth"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func enabled(key string) bool {
	return os.Getenv(key) == "1" || strings.EqualFold(os.Getenv(key), "true") || strings.EqualFold(os.Getenv(key), "yes")
}
func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	sessions := identity.SQLStore{DB: db}
	origins := identity.FrontendOriginsFromEnvironment(os.Getenv)
	callback := strings.TrimSpace(os.Getenv("GITHUB_OAUTH_REDIRECT_URI"))
	if callback == "" {
		callback = strings.TrimRight(os.Getenv("WEBHOOK_BASE_URL"), "/") + "/auth/github/callback"
	}
	oauthOrigins := []string{}
	for _, part := range strings.Split(os.Getenv("MCP_OAUTH_RESUME_BASE_URLS"), ",") {
		if strings.TrimSpace(part) != "" {
			oauthOrigins = append(oauthOrigins, strings.TrimSpace(part))
		}
	}
	apiOrigin := strings.TrimRight(os.Getenv("MCP_OAUTH_API_ORIGIN"), "/")
	if apiOrigin == "" && len(origins) > 0 {
		apiOrigin = origins[0]
	}
	if apiOrigin != "" {
		oauthOrigins = append(oauthOrigins, apiOrigin)
	}
	cipher, err := security.NewTokenCipher(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	loginConfig := identity.Config{ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), RedirectURI: callback, EncryptionKey: os.Getenv("ENCRYPTION_KEY"), FrontendOrigins: origins, OAuthReturnOrigins: oauthOrigins, AppEnvironment: os.Getenv("APP_ENV"), GitHubAppConfigured: os.Getenv("GITHUB_APP_SLUG") != "" && os.Getenv("WEBHOOK_BASE_URL") != "", AdminLogins: strings.Split(os.Getenv("INTERNAL_ADMIN_GITHUB_LOGINS"), ","), AdminIDs: identity.ParseAdminIDs(os.Getenv("INTERNAL_ADMIN_GITHUB_IDS"))}
	if enabled("ATPROTO_ENABLED") {
		key, e := atcrypto.ParsePrivateMultibase(os.Getenv("ATPROTO_OAUTH_PRIVATE_KEY"))
		if e != nil {
			log.Fatal("invalid ATProto OAuth assertion key")
		}
		collections := []string{}
		for _, v := range strings.Split(os.Getenv("ATPROTO_RECORD_COLLECTIONS"), ",") {
			if v = strings.TrimSpace(v); v != "" {
				collections = append(collections, v)
			}
		}
		permissions := []atidentity.ServicePermission{}
		if raw := os.Getenv("ATPROTO_SERVICE_PERMISSIONS"); raw != "" {
			if json.Unmarshal([]byte(raw), &permissions) != nil {
				log.Fatal("invalid ATPROTO_SERVICE_PERMISSIONS JSON")
			}
		}
		app, metadata, e := atidentity.NewClient(atidentity.ClientConfig{ClientID: os.Getenv("ATPROTO_CLIENT_ID"), CallbackURL: os.Getenv("ATPROTO_CALLBACK_URL"), Collections: collections, ServiceAuth: permissions, PrivateKey: key, KeyID: os.Getenv("ATPROTO_OAUTH_KEY_ID")}, &atidentity.Store{DB: db, Cipher: cipher})
		if e != nil {
			log.Fatal(e)
		}
		loginConfig.ATProto = &identity.ATProtoConfig{Client: atidentity.FlowClient{ClientApp: app}, Accounts: identity.ATProtoSQLStore{DB: db, Cipher: cipher}, Metadata: metadata, Handle: func(ctx context.Context, did syntax.DID) (string, error) {
			ident, e := app.Dir.LookupDID(ctx, did)
			if e != nil {
				return "", e
			}
			return ident.Handle.String(), nil
		}}
	}
	login, err := identity.New(loginConfig, sessions, identity.NewGitHubHTTP())
	if err != nil {
		log.Fatal(err)
	}
	client := &githubapp.Client{ClientID: os.Getenv("GITHUB_APP_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_APP_CLIENT_SECRET"), SetupCallback: os.Getenv("GITHUB_APP_SETUP_CALLBACK_URL")}
	if raw, encoded := os.Getenv("GITHUB_APP_PRIVATE_KEY"), os.Getenv("GITHUB_APP_PRIVATE_KEY_BASE64"); raw != "" || encoded != "" {
		client.Key, err = githubapp.LoadPrivateKey(raw, encoded)
		if err != nil {
			log.Fatal(err)
		}
	}
	github, err := githubapp.New(githubapp.Config{DB: db, Sessions: sessions, Client: client, Cipher: cipher, Slug: os.Getenv("GITHUB_APP_SLUG"), WebhookOrigin: os.Getenv("WEBHOOK_BASE_URL"), FrontendOrigins: origins})
	if err != nil {
		log.Fatal(err)
	}
	var authorization *oauth.Service
	if enabled("MCP_OAUTH_ENABLED") {
		path := os.Getenv("SAAS_MCP_PATH")
		if path == "" {
			path = os.Getenv("MCP_PATH")
		}
		store := mcp.SQLStore{DB: db}
		tenants, e := mcp.New(mcp.Config{Path: path, BaseDomain: os.Getenv("SAAS_MCP_BASE_DOMAIN"), TrustForwardedHost: enabled("MCP_TRUST_X_FORWARDED_HOST")}, store)
		if e != nil {
			log.Fatal(e)
		}
		authorization, e = oauth.New(oauth.Config{Enabled: true, AllowedOrigins: origins, MCPPath: tenants.Path(), APIOrigin: apiOrigin, GitHubLogin: login.MCPLoginURL, Resolve: func(r *http.Request) (*oauth.Project, error) {
			p, e := tenants.ResolveTenant(r)
			if e != nil || p == nil {
				return nil, e
			}
			return &oauth.Project{ID: p.ID, AccountID: p.AccountID, Name: p.Name, Origin: tenants.PublicOrigin(r)}, nil
		}, ProjectByID: func(ctx context.Context, id string) (*oauth.Project, error) {
			p, e := store.ProjectByID(ctx, id)
			if e != nil || p == nil {
				return nil, e
			}
			origin := ""
			if p.CustomDomain != "" {
				origin = "https://" + p.CustomDomain
			} else if p.Subdomain != "" && os.Getenv("SAAS_MCP_BASE_DOMAIN") != "" {
				origin = "https://" + p.Subdomain + "." + os.Getenv("SAAS_MCP_BASE_DOMAIN")
			}
			if _, e := url.ParseRequestURI(origin); e != nil {
				return nil, e
			}
			return &oauth.Project{ID: p.ID, AccountID: p.AccountID, Name: p.Name, Origin: origin}, nil
		}}, db, sessions)
		if e != nil {
			log.Fatal(e)
		}
	}
	handler := newIdentityRouter(login, github, authorization)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// The GitHub App may use the same user-authorization callback for ordinary
// sign-in and OAuth-on-install. Separate their state protocols before exchange.
func newIdentityRouter(login, github http.Handler, authorization *oauth.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorization != nil && oauth.Handles(r.URL.Path) {
			authorization.ServeHTTP(w, r)
			return
		}
		if githubapp.IsInstallationCallback(r) || strings.HasPrefix(r.URL.Path, "/github/") || strings.HasPrefix(r.URL.Path, "/auth/github/app/") {
			github.ServeHTTP(w, r)
			return
		}
		login.ServeHTTP(w, r)
	})
}
