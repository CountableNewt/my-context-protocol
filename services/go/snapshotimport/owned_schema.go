package snapshotimport

import (
	"errors"
	"regexp"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/appschema"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
)

const ownedRegistryDDL = "CREATE TABLE go_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL,applied_at REAL NOT NULL)"

var ifNotExistsPattern = regexp.MustCompile(`(?i)\bIF\s+NOT\s+EXISTS\s+`)

func normalizedCreate(sql string) string {
	return normalized(ifNotExistsPattern.ReplaceAllString(sql, ""))
}

func additiveMigrations() []migrations.Migration { return appschema.Registry() }

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
