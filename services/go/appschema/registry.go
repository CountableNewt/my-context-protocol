// Package appschema defines the explicit shipped application migration registry.
package appschema

import (
	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/atplatform"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/control"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/providerhub"
	"github.com/CountableNewt/my-context-protocol/services/go/recordservice"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpcauth"
)

// Registry returns a fresh immutable-by-convention ordered registry. No migration runs here.
func Registry() []migrations.Migration {
	return []migrations.Migration{{Version: "002_mcp_shared_state", SQL: mcp.MigrationSQL}, {Version: "003_sync_jobs", SQL: syncservice.MigrationSQL}, {Version: "004_github_operations", SQL: githubapp.MigrationSQL}, {Version: "005_maintenance_indexes", SQL: control.MaintenanceSQL}, {Version: "006_atproto_private_oauth", SQL: atidentity.MigrationSQL}, {Version: "007_atproto_publication_intents", SQL: atproto.MigrationSQL}, {Version: migrations.AccountIdentityVersion, SQL: migrations.AccountIdentitySQL}, {Version: "009_atproto_account_links", SQL: identity.ATProtoMigrationSQL}, {Version: "010_atproto_session_leases", SQL: ataccount.MigrationSQL}, {Version: "011_atproto_xrpc_replays", SQL: xrpcauth.MigrationSQL}, {Version: "012_tangled_repository_bindings", SQL: providerhub.MigrationSQL}, {Version: "013_canonical_domain_records", SQL: domainrecords.MigrationSQL}, {Version: "014_canonical_native_intents", SQL: recordservice.MigrationSQL}, {Version: "015_sync_publication_plans", SQL: syncservice.PublicationMigrationSQL}, {Version: "016_tangled_attestation_intents", SQL: atplatform.MigrationSQL}}
}
