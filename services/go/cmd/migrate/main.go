// migrate is an explicit operator command. It never runs as a service hook.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/appschema"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	initialize := flag.Bool("initialize", false, "Initialize an empty database; never overwrite existing application tables")
	adopt := flag.Bool("adopt-legacy", false, "Verify and adopt an imported Swift schema without replaying its DDL")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected migration arguments")
	}
	db, e := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e = migrations.Apply(ctx, db, migrations.Options{Initialize: *initialize, AdoptLegacy: *adopt, Additions: appschema.Registry()})
	if e != nil {
		log.Fatal(e)
	}
	fmt.Println("Schema migration complete. No account data was replaced.")
}
