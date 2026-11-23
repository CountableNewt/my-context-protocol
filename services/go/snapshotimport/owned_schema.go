package snapshotimport

import (
	"errors"
	"regexp"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/control"
	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpcauth"
)

const ownedRegistryDDL = "CREATE TABLE go_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL,applied_at REAL NOT NULL)"

var ifNotExistsPattern = regexp.MustCompile(`(?i)\bIF\s+NOT\s+EXISTS\s+`)

func normalizedCreate(sql string) string {
	return normalized(ifNotExistsPattern.ReplaceAllString(sql, ""))
}

func additiveMigrations() []migrations.Migration {
	return []migrations.Migration{{Version: "002_mcp_shared_state", SQL: mcp.MigrationSQL}, {Version: "003_sync_jobs", SQL: syncservice.MigrationSQL}, {Version: "004_github_operations", SQL: githubapp.MigrationSQL}, {Version: "005_maintenance_indexes", SQL: control.MaintenanceSQL}, {Version: "006_atproto_private_oauth", SQL: atidentity.MigrationSQL}, {Version: "007_atproto_publication_intents", SQL: atproto.MigrationSQL}, {Version: migrations.AccountIdentityVersion, SQL: migrations.AccountIdentitySQL}, {Version: "009_atproto_account_links", SQL: identity.ATProtoMigrationSQL}, {Version: "010_atproto_session_leases", SQL: ataccount.MigrationSQL}, {Version: "011_atproto_xrpc_replays", SQL: xrpcauth.MigrationSQL}}
}

// Only shipped schema objects can accompany a staged import. In particular,
// unknown triggers must never execute as a side effect of resumed inserts.
func ownedSchema() (map[string]string, error) {
	expected, _, e := baseline()
	if e != nil {
		return nil, e
	}
	scripts := []string{ownedRegistryDDL, runDDL, progressDDL}
	for _, addition := range additiveMigrations() {
		if addition.Version == migrations.AccountIdentityVersion {
			continue
		}
		scripts = append(scripts, addition.SQL)
	}
	for _, script := range scripts {
		statements, e := migrations.SplitSQL(script)
		if e != nil {
			return nil, e
		}
		for _, sql := range statements {
			parts := createPattern.FindStringSubmatch(sql)
			if len(parts) != 3 {
				return nil, errors.New("unsupported owned schema definition")
			}
			name := strings.Trim(parts[2], `"`)
			ddl := normalizedCreate(sql)
			if prior, exists := expected[name]; exists && normalizedCreate(prior) != ddl {
				return nil, errors.New("conflicting owned schema definition")
			}
			expected[name] = ddl
		}
	}
	return expected, nil
}
