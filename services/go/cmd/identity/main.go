package main

import (
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	origins := identity.FrontendOriginsFromEnvironment(os.Getenv)
	callback := strings.TrimSpace(os.Getenv("GITHUB_OAUTH_REDIRECT_URI"))
	if callback == "" {
		callback = strings.TrimRight(os.Getenv("WEBHOOK_BASE_URL"), "/") + "/auth/github/callback"
	}
	var oauthOrigins []string
	for _, value := range strings.Split(os.Getenv("MCP_OAUTH_RESUME_BASE_URLS"), ",") {
		if strings.TrimSpace(value) != "" {
			oauthOrigins = append(oauthOrigins, strings.TrimSpace(value))
		}
	}
	if value := strings.TrimSpace(os.Getenv("MCP_OAUTH_API_ORIGIN")); value != "" {
		oauthOrigins = append(oauthOrigins, value)
	}
	handler, err := identity.New(identity.Config{ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), RedirectURI: callback, EncryptionKey: os.Getenv("ENCRYPTION_KEY"), FrontendOrigins: origins, OAuthReturnOrigins: oauthOrigins, AppEnvironment: os.Getenv("APP_ENV"), GitHubAppConfigured: os.Getenv("GITHUB_APP_SLUG") != "" && os.Getenv("WEBHOOK_BASE_URL") != "", AdminLogins: strings.Split(os.Getenv("INTERNAL_ADMIN_GITHUB_LOGINS"), ","), AdminIDs: identity.ParseAdminIDs(os.Getenv("INTERNAL_ADMIN_GITHUB_IDS"))}, identity.SQLStore{DB: db}, identity.NewGitHubHTTP())
	if err != nil {
		log.Fatal(err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
