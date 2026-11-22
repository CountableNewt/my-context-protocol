package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
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
	app, err := githubapp.New(githubapp.Config{DB: db, Sessions: identity.SQLStore{DB: db}, Client: client, Cipher: cipher, Slug: os.Getenv("GITHUB_APP_SLUG"), WebhookOrigin: os.Getenv("WEBHOOK_BASE_URL"), FrontendOrigins: identity.FrontendOriginsFromEnvironment(os.Getenv), AppWebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET"), PushWebhook: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if syncHandler == nil {
			http.Error(w, "Sync service unavailable", 503)
			return
		}
		syncHandler.ServeHTTP(w, r)
	})})
	if err != nil {
		log.Fatal(err)
	}
	service, err := syncservice.New(syncservice.Config{Store: syncservice.SQLStore{DB: db}, Fetcher: reposync.Fetcher{}, DecryptToken: cipher.Decrypt, InstallationToken: app.InstallationToken, InternalToken: os.Getenv("INTERNAL_SERVICE_TOKEN"), WebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET")})
	if err != nil {
		log.Fatal(err)
	}
	syncHandler = service
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhooks/github-app" {
			app.ServeHTTP(w, r)
			return
		}
		service.ServeHTTP(w, r)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 80 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
