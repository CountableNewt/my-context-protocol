package migrations

import (
	"context"
	"errors"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const AccountIdentityVersion = "008_accounts_atproto_identity"

// AccountIdentityDDL differs from the historical baseline only by allowing an
// account without a GitHub identity. Never rewrite the recorded baseline.
var AccountIdentityDDL = strings.Replace(baselineAccountDDL(), `"github_id" INTEGER NOT NULL`, `"github_id" INTEGER`, 1)
var AccountIdentitySQL = strings.Join([]string{
	strings.Replace(AccountIdentityDDL, `CREATE TABLE "accounts"`, `CREATE TABLE "accounts_atproto_rebuild"`, 1),
	`INSERT INTO accounts_atproto_rebuild SELECT * FROM accounts`,
	`DROP TABLE accounts`,
	`ALTER TABLE accounts_atproto_rebuild RENAME TO accounts`,
}, ";\n") + ";"

func baselineAccountDDL() string {
	statements, err := SplitSQL(BaselineSQL)
	if err != nil {
		panic("invalid shipped baseline")
	}
	for _, sql := range statements {
		if strings.HasPrefix(sql, `CREATE TABLE "accounts" `) {
			return sql
		}
	}
	panic("shipped baseline has no accounts table")
}

type schemaDatabase interface {
	SchemaBatch(context.Context, []turso.Statement) ([]turso.Result, error)
}

func rebuildAccounts(ctx context.Context, db Database, migration Migration) error {
	if migration.SQL != AccountIdentitySQL {
		return errors.New("account identity migration differs from shipped rebuild")
	}
	schemaDB, ok := db.(schemaDatabase)
	if !ok {
		return errors.New("account identity migration requires guarded schema transactions")
	}
	r, err := db.Execute(ctx, turso.Statement{SQL: "SELECT sql FROM sqlite_schema WHERE type='table' AND name='accounts'", WantRows: true})
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		return errors.New("account schema is missing")
	}
	actual, ok := r.Rows[0][0].Value.(string)
	if !ok || normalize(actual) != normalize(baselineAccountDDL()) {
		return errors.New("account identity migration requires exact historical account schema")
	}
	// Recheck the observed DDL inside the write transaction to reject a concurrent
	// migration between inspection and rebuild. Child rows never see a dropped
	// parent while FK actions are enabled; the transaction checks all FKs at end.
	statements := []turso.Statement{
		{SQL: "CREATE TEMP TABLE account_schema_guard(ok INTEGER NOT NULL CHECK(ok=1))"},
		{SQL: "INSERT INTO account_schema_guard SELECT CASE WHEN COUNT(*)=1 AND MIN(sql)=? THEN 1 ELSE 0 END FROM sqlite_schema WHERE type='table' AND name='accounts'", Args: []turso.Value{turso.Text(actual)}},
		{SQL: "DROP TABLE account_schema_guard"},
	}
	parts, err := SplitSQL(migration.SQL)
	if err != nil {
		return err
	}
	for _, sql := range parts {
		statements = append(statements, turso.Statement{SQL: sql})
	}
	statements = append(statements, record(migration.Version, migration.SQL))
	_, err = schemaDB.SchemaBatch(ctx, statements)
	return err
}
