package atplatform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const MigrationSQL = `CREATE TABLE tangled_attestation_intents(owner_did TEXT NOT NULL,subject_uri TEXT NOT NULL,subject_cid TEXT NOT NULL,record_key TEXT NOT NULL,created_at TEXT NOT NULL,git_change TEXT NOT NULL,PRIMARY KEY(owner_did,subject_uri,subject_cid));`

type attestor struct {
	platform *Platform
	account  string
}

func (a *attestor) Attest(ctx context.Context, actor, uri, cid, change string) error {
	linked, err := a.platform.Accounts.Identities.ATProtoIdentity(ctx, a.account)
	if err != nil {
		return err
	}
	if linked == nil || linked.DID != actor {
		return errors.New("Tangled attestation actor does not own account")
	}
	if _, err = syntax.ParseATURI(uri); err != nil {
		return err
	}
	if _, err = syntax.ParseCID(cid); err != nil {
		return err
	}
	now := time.Now().UTC()
	key := syntax.NewTIDFromTime(now, 0).String()
	r, err := a.platform.Raw.Batch(ctx, []turso.Statement{
		{SQL: `INSERT INTO tangled_attestation_intents(owner_did,subject_uri,subject_cid,record_key,created_at,git_change) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, Args: []turso.Value{turso.Text(actor), turso.Text(uri), turso.Text(cid), turso.Text(key), turso.Text(now.Format(time.RFC3339Nano)), turso.Text(change)}},
		{SQL: `SELECT record_key,created_at,git_change FROM tangled_attestation_intents WHERE owner_did=? AND subject_uri=? AND subject_cid=?`, Args: []turso.Value{turso.Text(actor), turso.Text(uri), turso.Text(cid)}, WantRows: true},
	})
	if err != nil {
		return err
	}
	if len(r) != 2 || len(r[1].Rows) != 1 || len(r[1].Rows[0]) != 3 {
		return errors.New("attestation intent was not persisted")
	}
	row := r[1].Rows[0]
	key = fmt.Sprint(row[0].Value)
	if fmt.Sprint(row[2].Value) != change {
		return atproto.ErrConflict
	}
	record := map[string]any{"$type": "org.tangled.graph.attestation", "subject": map[string]string{"uri": uri, "cid": cid}, "createdAt": fmt.Sprint(row[1].Value)}
	if change != "" {
		record["change"] = change
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return a.platform.withRecords(ctx, a.account, func(ctx context.Context, records atproto.Records) error {
		_, err := (atproto.Publisher{Records: records, Journal: atproto.SQLJournal{DB: a.platform.Raw}}).Publish(ctx, actor, "tangled-attestation:"+key, atproto.Mutation{Repo: actor, Collection: "org.tangled.graph.attestation", RKey: key, Record: raw})
		return err
	})
}
