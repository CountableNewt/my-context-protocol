package operatorjob

import (
	"context"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"regexp"
	"strconv"
	"strings"
)

var prefixPattern = regexp.MustCompile(`^operatorjob:[0-9a-f]{64}:$`)

// SnapshotDatabase excludes only this freshly generated recipient's disposable
// coordination rows. Every normal or other operator bucket remains byte-exact.
func SnapshotDatabase(db Database, prefix string) Database {
	return snapshotDatabase{Database: db, prefix: prefix}
}

type snapshotDatabase struct {
	Database
	prefix string
}

func (d snapshotDatabase) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	if !prefixPattern.MatchString(d.prefix) {
		return nil, errors.New("invalid exact operator snapshot prefix")
	}
	// ExportRemote uses this one quoted identifier in counts, typed reads and
	// drift verification. Preserve rowid for bounded source-keyset pagination.
	const source = `FROM "mcp_rate_limits"`
	replacement := `FROM (SELECT rowid,* FROM "mcp_rate_limits" WHERE substr(bucket_key,1,` + strconv.Itoa(len(d.prefix)) + `) <> '` + d.prefix + `')`
	s.SQL = strings.ReplaceAll(s.SQL, source, replacement)
	return d.Database.Execute(ctx, s)
}
func (d snapshotDatabase) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	return nil, errors.New("operator snapshot adapter is read-only")
}
