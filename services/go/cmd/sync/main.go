package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atplatform"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpcauth"
)

func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	platform, err := atplatform.New(db, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	var appDB syncservice.Database = db
	if platform != nil {
		appDB = platform.DB
	}
	cipher, err := security.NewTokenCipher(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Getenv("INTERNAL_SERVICE_TOKEN")) < 32 {
		log.Fatal("INTERNAL_SERVICE_TOKEN must contain at least 32 characters")
	}
	client := &githubapp.Client{ClientID: os.Getenv("GITHUB_APP_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_APP_CLIENT_SECRET"), SetupCallback: os.Getenv("GITHUB_APP_SETUP_CALLBACK_URL")}
	rawKey, encodedKey := os.Getenv("GITHUB_APP_PRIVATE_KEY"), os.Getenv("GITHUB_APP_PRIVATE_KEY_BASE64")
	if rawKey != "" || encodedKey != "" {
		client.Key, err = githubapp.LoadPrivateKey(rawKey, encodedKey)
		if err != nil {
			log.Fatal(err)
		}
	}
	var syncHandler http.Handler
	app, err := githubapp.New(githubapp.Config{DB: appDB, Sessions: identity.SQLStore{DB: db}, Client: client, Cipher: cipher, Slug: os.Getenv("GITHUB_APP_SLUG"), WebhookOrigin: os.Getenv("WEBHOOK_BASE_URL"), FrontendOrigins: identity.FrontendOriginsFromEnvironment(os.Getenv), AppWebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET"), PushWebhook: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if syncHandler == nil {
			http.Error(w, "Sync service unavailable", 503)
			return
		}
		syncHandler.ServeHTTP(w, r)
	})})
	if err != nil {
		log.Fatal(err)
	}
	config := syncservice.Config{Store: syncservice.SQLStore{DB: appDB}, Fetcher: reposync.Fetcher{}, DecryptToken: cipher.Decrypt, InstallationToken: app.InstallationToken, InternalToken: os.Getenv("INTERNAL_SERVICE_TOKEN"), WebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET")}
	var service *syncservice.Service
	if platform != nil {
		hub, e := platform.Repositories(app.ConnectRepository, app.Writeback, func(ctx context.Context, pid string) (map[string]any, error) {
			if service == nil {
				return nil, errors.New("sync is not configured")
			}
			return service.SyncProject(ctx, pid)
		})
		if e != nil {
			log.Fatal(e)
		}
		config.Providers = map[string]syncservice.RepositoryProvider{"tangled": hub.Provider()}
		config.ProjectContext = platform.ScopeProject
	}
	service, err = syncservice.New(config)
	if err != nil {
		log.Fatal(err)
	}
	syncHandler = service
	handler := newSyncRouter(service, app)
	verifier, err := xrpcauth.FromEnvironment(db, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if verifier != nil {
		rpc, e := service.XRPCHandler(verifier)
		if e != nil {
			log.Fatal(e)
		}
		handler = xrpcauth.Route(handler, rpc)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 80 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// Existing GitHub App registrations may share /webhooks/github with repository
// hooks. Dispatch only known App lifecycle events; the App handler still checks
// its own raw-body signature before any state mutation.
func newSyncRouter(syncHandler, appHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhooks/github-app" {
			appHandler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/webhooks/github" {
			switch r.Header.Get("X-GitHub-Event") {
			case "installation", "installation_repositories", "github_app_authorization":
				clone := r.Clone(r.Context())
				target := *r.URL
				target.Path = "/webhooks/github-app"
				clone.URL = &target
				appHandler.ServeHTTP(w, clone)
				return
			}
		}
		syncHandler.ServeHTTP(w, r)
	})
}
