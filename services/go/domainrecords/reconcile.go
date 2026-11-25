package domainrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func decodeEntity(project string, fields map[string]json.RawMessage) (Entry, error) {
	var collection, id, projectID string
	json.Unmarshal(fields["$type"], &collection)
	json.Unmarshal(fields["legacyId"], &id)
	json.Unmarshal(fields["projectId"], &projectID)
	if projectID != project {
		return Entry{}, errors.New("cross-project canonical record")
	}
	d, err := byCollection(collection)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{Table: d.Table, ID: id, Values: map[string]turso.Value{}}
	allowed := map[string]Field{}
	for _, f := range d.Fields {
		allowed[f.Name] = f
	}
	for name, raw := range fields {
		if name == "$type" || name == "legacyId" || name == "projectId" {
			continue
		}
		f, ok := allowed[name]
		if !ok {
			return Entry{}, errors.New("unknown canonical field")
		}
		v, err := decodeValue(f, raw)
		if err != nil {
			return Entry{}, err
		}
		entry.Values[name] = v
	}
	return entry, nil
}
func (e Engine) rebuildReview(ctx context.Context, owner, project string, m manifest, ref atproto.Reference) (Review, error) {
	_, err := e.DB.Execute(ctx, statement("INSERT INTO domain_reconciliation_runs(project_id,manifest_cid,owner_did) VALUES(?,?,?) ON CONFLICT(project_id,manifest_cid) DO NOTHING", turso.Text(project), turso.Text(ref.CID), turso.Text(owner)))
	if err != nil {
		return Review{}, err
	}
	cursor, err := e.DB.Execute(ctx, statement("SELECT owner_did,shard_index,entry_index FROM domain_reconciliation_runs WHERE project_id=? AND manifest_cid=?", turso.Text(project), turso.Text(ref.CID)))
	if err != nil {
		return Review{}, err
	}
	if len(cursor.Rows) != 1 || len(cursor.Rows[0]) != 3 || fmt.Sprint(cursor.Rows[0][0].Value) != owner {
		return Review{}, errors.New("invalid reconciliation owner")
	}
	shardIndex, err := strconv.Atoi(fmt.Sprint(cursor.Rows[0][1].Value))
	if err != nil || shardIndex < 0 || shardIndex > len(m.Refs) {
		return Review{}, errors.New("invalid reconciliation shard cursor")
	}
	entryIndex, err := strconv.Atoi(fmt.Sprint(cursor.Rows[0][2].Value))
	if err != nil || entryIndex < 0 || entryIndex > 100 {
		return Review{}, errors.New("invalid reconciliation entry cursor")
	}
	limit := e.WorkLimit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	used := 0
	for shardIndex < len(m.Refs) {
		if used >= limit {
			return Review{}, &PendingError{Completed: shardIndex*100 + entryIndex, Total: len(m.Refs) * 100, Digest: m.Digest}
		}
		var shard manifest
		if err = e.fetch(ctx, owner, m.Refs[shardIndex], &shard); err != nil {
			return Review{}, err
		}
		if shard.Type != Namespace+"manifestShard" || shard.ProjectID != project || shard.Digest != refDigest(shard.Refs) || len(shard.Refs) > 100 {
			return Review{}, fmt.Errorf("%w: invalid manifest shard", ErrInvalidCanonical)
		}
		shardRaw, _ := json.Marshal(shard)
		shardKey := atproto.StableKey(shard.Type, digest(shardRaw))
		if m.Refs[shardIndex].URI != "at://"+owner+"/"+shard.Type+"/"+shardKey {
			return Review{}, fmt.Errorf("%w: invalid shard identity", ErrInvalidCanonical)
		}
		if err = e.cacheVersion(ctx, Review{OwnerDID: owner, ProjectID: project, Digest: m.Digest}, shard.Type, shardKey, m.Refs[shardIndex]); err != nil {
			return Review{}, err
		}
		for entryIndex < len(shard.Refs) {
			if used >= limit {
				return Review{}, &PendingError{Completed: shardIndex*100 + entryIndex, Total: len(m.Refs) * 100, Digest: m.Digest}
			}
			entityRef := shard.Refs[entryIndex]
			var fields map[string]json.RawMessage
			if err = e.fetch(ctx, owner, entityRef, &fields); err != nil {
				return Review{}, err
			}
			used++
			entry, err := decodeEntity(project, fields)
			if err != nil {
				return Review{}, fmt.Errorf("%w: %v", ErrInvalidCanonical, err)
			}
			encoded, err := encode(project, entry)
			if err != nil {
				return Review{}, fmt.Errorf("%w: %v", ErrInvalidCanonical, err)
			}
			if entityRef.URI != "at://"+owner+"/"+encoded.Collection+"/"+encoded.Key {
				return Review{}, fmt.Errorf("%w: invalid entity identity", ErrInvalidCanonical)
			}
			if err = e.cacheVersion(ctx, Review{OwnerDID: owner, ProjectID: project, Digest: m.Digest}, encoded.Collection, encoded.Key, entityRef); err != nil {
				return Review{}, err
			}
			raw, _ := json.Marshal(entry)
			results, err := e.DB.Batch(ctx, []turso.Statement{statement("INSERT INTO domain_reconciliation_entries(project_id,manifest_cid,table_name,row_id,entry_json) VALUES(?,?,?,?,?) ON CONFLICT(project_id,manifest_cid,table_name,row_id) DO UPDATE SET entry_json=excluded.entry_json", turso.Text(project), turso.Text(ref.CID), turso.Text(entry.Table), turso.Text(entry.ID), turso.Text(string(raw))), statement("UPDATE domain_reconciliation_runs SET entry_index=? WHERE project_id=? AND manifest_cid=? AND shard_index=? AND entry_index=? RETURNING entry_index", turso.Integer(int64(entryIndex+1)), turso.Text(project), turso.Text(ref.CID), turso.Integer(int64(shardIndex)), turso.Integer(int64(entryIndex)))})
			if err != nil {
				return Review{}, err
			}
			if len(results) != 2 || len(results[1].Rows) != 1 {
				return Review{}, errors.New("reconciliation cursor changed")
			}
			entryIndex++
		}
		if _, err = e.DB.Execute(ctx, statement("UPDATE domain_reconciliation_runs SET shard_index=?,entry_index=0 WHERE project_id=? AND manifest_cid=? AND shard_index=?", turso.Integer(int64(shardIndex+1)), turso.Text(project), turso.Text(ref.CID), turso.Integer(int64(shardIndex)))); err != nil {
			return Review{}, err
		}
		shardIndex++
		entryIndex = 0
	}
	review := Review{OwnerDID: owner, ProjectID: project, Entries: []Entry{}}
	lastTable, lastID := "", ""
	pageSize := 1000
	for {
		rows, err := e.DB.Execute(ctx, statement("SELECT table_name,row_id,entry_json FROM domain_reconciliation_entries WHERE project_id=? AND manifest_cid=? AND (table_name>? OR (table_name=? AND row_id>?)) ORDER BY table_name,row_id LIMIT ?", turso.Text(project), turso.Text(ref.CID), turso.Text(lastTable), turso.Text(lastTable), turso.Text(lastID), turso.Integer(int64(pageSize))))
		if err != nil {
			if pageSize > 1 && strings.Contains(err.Error(), "response exceeds") {
				pageSize = max(1, pageSize/2)
				continue
			}
			return Review{}, err
		}
		for _, row := range rows.Rows {
			if len(row) != 3 {
				return Review{}, errors.New("invalid staged canonical row")
			}
			var entry Entry
			if json.Unmarshal([]byte(fmt.Sprint(row[2].Value)), &entry) != nil {
				return Review{}, errors.New("invalid staged canonical entry")
			}
			review.Entries = append(review.Entries, entry)
			d, err := definition(entry.Table)
			if err != nil {
				return Review{}, err
			}
			review.HasTelemetry = review.HasTelemetry || hasTelemetry(entry, d)
			lastTable, lastID = fmt.Sprint(row[0].Value), fmt.Sprint(row[1].Value)
			if len(review.Entries) > MaxEntries {
				return Review{}, errors.New("canonical record count exceeds limit")
			}
		}
		if len(rows.Rows) < pageSize {
			break
		}
	}
	review, err = finalize(review)
	if err != nil {
		return Review{}, fmt.Errorf("%w: %v", ErrInvalidCanonical, err)
	}
	if review.Digest != m.Digest {
		return Review{}, fmt.Errorf("%w: content digest mismatch", ErrInvalidCanonical)
	}
	return review, nil
}
