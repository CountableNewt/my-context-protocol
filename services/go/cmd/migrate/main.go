// migrate is an explicit operator command. It never runs as a service hook.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/control"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpcauth"
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
	e = migrations.Apply(ctx, db, migrations.Options{Initialize: *initialize, AdoptLegacy: *adopt, Additions: []migrations.Migration{{Version: "002_mcp_shared_state", SQL: mcp.MigrationSQL}, {Version: "003_sync_jobs", SQL: syncservice.MigrationSQL}, {Version: "004_github_operations", SQL: githubapp.MigrationSQL}, {Version: "005_maintenance_indexes", SQL: control.MaintenanceSQL}, {Version: "006_atproto_private_oauth", SQL: atidentity.MigrationSQL}, {Version: "007_atproto_publication_intents", SQL: atproto.MigrationSQL}, {Version: migrations.AccountIdentityVersion, SQL: migrations.AccountIdentitySQL}, {Version: "009_atproto_account_links", SQL: identity.ATProtoMigrationSQL}, {Version: "010_atproto_session_leases", SQL: ataccount.MigrationSQL}, {Version: "011_atproto_xrpc_replays", SQL: xrpcauth.MigrationSQL}}})
	if e != nil {
		log.Fatal(e)
	}
	fmt.Println("Schema migration complete. No account data was replaced.")
}
