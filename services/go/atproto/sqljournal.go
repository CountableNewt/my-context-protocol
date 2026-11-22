package atproto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// MigrationSQL is applied explicitly by the schema tooling, never at startup.
const MigrationSQL = `
CREATE TABLE IF NOT EXISTS atproto_publication_intents (
 owner_did TEXT NOT NULL,
 intent_key TEXT NOT NULL,
 digest TEXT NOT NULL,
 mutation_json TEXT NOT NULL,
 record_uri TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','complete')),
 canonical_cid TEXT,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(owner_did,intent_key),
 CHECK((status='pending' AND canonical_cid IS NULL) OR (status='complete' AND length(canonical_cid)>0))
);
CREATE INDEX IF NOT EXISTS atproto_publication_pending ON atproto_publication_intents(owner_did,status,intent_key);
`

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}

// SQLJournal stores private durable publication intents. Its caller must prove
// that the linked account owns owner_did and has consented to publication.
// Journal contents can contain private unpublished data: never expose this table
// through public record indexing or an unauthenticated reconciliation endpoint.
type SQLJournal struct{ DB Database }

type PendingIntent struct {
	Owner    string
	Key      string
	Digest   string
	Mutation Mutation
}

func journalStatement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}

func textValue(row []turso.Value, index int) (string, error) {
	if index >= len(row) || row[index].Type != "text" {
		return "", errors.New("invalid publication journal row")
	}
	value, ok := row[index].Value.(string)
	if !ok {
		return "", errors.New("invalid publication journal value")
	}
	return value, nil
}

func validateIntent(owner, key, digest string, m Mutation) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if owner != m.Repo || strings.TrimSpace(key) == "" || len(key) > 512 || strings.ContainsAny(key, "\x00\r\n") {
		return nil, errors.New("invalid publication intent owner or key")
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("publication intent exceeds byte limit")
	}
	sum := sha256.Sum256(data)
	if digest != hex.EncodeToString(sum[:]) {
		return nil, errors.New("publication digest does not match mutation")
	}
	return data, nil
}

func (j SQLJournal) Reserve(ctx context.Context, owner, key, digest string, m Mutation) (*Reference, error) {
	if j.DB == nil {
		return nil, errors.New("publication journal database required")
	}
	data, err := validateIntent(owner, key, digest, m)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Unix()
	results, err := j.DB.Batch(ctx, []turso.Statement{
		journalStatement(`INSERT INTO atproto_publication_intents(owner_did,intent_key,digest,mutation_json,record_uri,status,created_at,updated_at) VALUES (?,?,?,?,?,'pending',?,?) ON CONFLICT(owner_did,intent_key) DO NOTHING`, turso.Text(owner), turso.Text(key), turso.Text(digest), turso.Text(string(data)), turso.Text(m.URI()), turso.Integer(now), turso.Integer(now)),
		journalStatement(`SELECT digest,status,record_uri,canonical_cid FROM atproto_publication_intents WHERE owner_did=? AND intent_key=?`, turso.Text(owner), turso.Text(key)),
	})
	if err != nil {
		return nil, err
	}
	if len(results) != 2 || len(results[1].Rows) != 1 {
		return nil, errors.New("publication reservation was not persisted")
	}
	row := results[1].Rows[0]
	stored, err := textValue(row, 0)
	if err != nil {
		return nil, err
	}
	if stored != digest {
		return nil, ErrConflict
	}
	status, err := textValue(row, 1)
	if err != nil {
		return nil, err
	}
	uri, err := textValue(row, 2)
	if err != nil {
		return nil, err
	}
	if uri != m.URI() {
		return nil, ErrConflict
	}
	if status == "pending" {
		return nil, nil
	}
	if status != "complete" {
		return nil, errors.New("invalid publication journal status")
	}
	cid, err := textValue(row, 3)
	if err != nil || !validCID(cid) {
		return nil, errors.New("invalid completed publication journal reference")
	}
	return &Reference{URI: uri, CID: cid}, nil
}

func (j SQLJournal) Complete(ctx context.Context, owner, key, digest string, ref Reference) error {
	if j.DB == nil {
		return errors.New("publication journal database required")
	}
	if owner == "" || key == "" || len(digest) != 64 || !validCID(ref.CID) || !strings.HasPrefix(ref.URI, "at://"+owner+"/") {
		return errors.New("invalid publication completion")
	}
	result, err := j.DB.Execute(ctx, journalStatement(`UPDATE atproto_publication_intents SET status='complete',canonical_cid=?,updated_at=? WHERE owner_did=? AND intent_key=? AND digest=? AND record_uri=? AND (status='pending' OR (status='complete' AND canonical_cid=?)) RETURNING canonical_cid`, turso.Text(ref.CID), turso.Integer(time.Now().UTC().Unix()), turso.Text(owner), turso.Text(key), turso.Text(digest), turso.Text(ref.URI), turso.Text(ref.CID)))
	if err != nil {
		return err
	}
	if result == nil || len(result.Rows) != 1 {
		return ErrConflict
	}
	cid, err := textValue(result.Rows[0], 0)
	if err != nil {
		return err
	}
	if cid != ref.CID {
		return ErrConflict
	}
	return nil
}

// Pending returns one owner's bounded page in stable key order. The last Key is
// the next cursor; start subsequent reconciliation sweeps with an empty cursor
// so previously failed intents are retried. The caller must supply an authorized
// owner DID and must not accept arbitrary DIDs from an unauthenticated request.
func (j SQLJournal) Pending(ctx context.Context, owner, after string, limit int) ([]PendingIntent, error) {
	if j.DB == nil {
		return nil, errors.New("publication journal database required")
	}
	if !strings.HasPrefix(owner, "did:") || strings.ContainsAny(owner, "/?# \t\r\n") || limit < 1 || limit > 100 || len(after) > 512 {
		return nil, errors.New("invalid publication reconciliation page")
	}
	result, err := j.DB.Execute(ctx, journalStatement(`SELECT intent_key,digest,mutation_json FROM atproto_publication_intents WHERE owner_did=? AND status='pending' AND intent_key>? ORDER BY intent_key LIMIT ?`, turso.Text(owner), turso.Text(after), turso.Integer(int64(limit))))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("missing publication reconciliation result")
	}
	out := make([]PendingIntent, 0, len(result.Rows))
	for _, row := range result.Rows {
		key, e := textValue(row, 0)
		if e != nil {
			return nil, e
		}
		digest, e := textValue(row, 1)
		if e != nil {
			return nil, e
		}
		raw, e := textValue(row, 2)
		if e != nil {
			return nil, e
		}
		var m Mutation
		if e = json.Unmarshal([]byte(raw), &m); e != nil {
			return nil, errors.New("invalid stored publication mutation")
		}
		if _, e = validateIntent(owner, key, digest, m); e != nil {
			return nil, fmt.Errorf("invalid stored publication intent: %w", e)
		}
		out = append(out, PendingIntent{Owner: owner, Key: key, Digest: digest, Mutation: m})
	}
	return out, nil
}
