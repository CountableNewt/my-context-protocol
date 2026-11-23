// operator-service is a temporary, unaliased production operator endpoint.
package main

import (
	"encoding/base64"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorjob"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	const project = "prj_jC1eRUJ0CHZvHp7Eej3AUAS4yUq7"
	const host = "my-context-protocol-production-countablenewt.aws-us-east-2.turso.io"
	if os.Getenv("VERCEL_ENV") != "production" || os.Getenv("VERCEL_PROJECT_ID") != project {
		log.Fatal("operator requires exact production project")
	}
	if operatorjob.ExpectedHost(host, os.Getenv("TURSO_DATABASE_URL")) != nil {
		log.Fatal("operator target mismatch")
	}
	raw, err := base64.StdEncoding.DecodeString(os.Getenv("OPERATOR_RECIPIENT_PUBLIC_KEY_BASE64"))
	if err != nil {
		log.Fatal("operator recipient missing")
	}
	key, err := operatorbackup.Public(raw)
	if err != nil {
		log.Fatal("operator recipient invalid")
	}
	db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if err != nil {
		log.Fatal("operator database config invalid")
	}
	handler, err := operatorservice.New(operatorservice.Config{DB: db, PublicKey: key, Host: host, Audience: os.Getenv("VERCEL_URL")})
	if err != nil {
		log.Fatal("operator service config invalid")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 260 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	log.Fatal(server.ListenAndServe())
}
