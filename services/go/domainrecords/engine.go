package domainrecords

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const MaxEntries = 100000

var ErrConsent = errors.New("explicit public publication and telemetry consent are required")
var ErrChanged = errors.New("project changed after publication review")
var ErrNotMigrated = errors.New("project has not been migrated to canonical records")
var ErrInvalidCanonical = errors.New("canonical record graph is invalid")

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Entry struct {
	Table  string                 `json:"table"`
	ID     string                 `json:"id"`
	Values map[string]turso.Value `json:"values"`
}
type Review struct {
	OwnerDID     string   `json:"owner_did"`
	ProjectID    string   `json:"project_id"`
	Digest       string   `json:"digest"`
	Entries      []Entry  `json:"entries"`
	Warnings     []string `json:"warnings"`
	HasTelemetry bool     `json:"has_telemetry"`
}
type Consent struct {
	ReviewDigest string `json:"review_digest"`
	Public       bool   `json:"public"`
	Telemetry    bool   `json:"telemetry"`
}
type Change struct {
	Table, ID string
	Values    map[string]turso.Value
	Delete    bool
}
type Engine struct {
	ExpectedPriorCID string
	ExpectedDigest   string
	BeforePublish    func(context.Context, Review) error
	// NativeProjection loads a durable, evaluated application intent. The
	// returned statements must include a source-manifest CAS guard. It is never
	// populated from user-supplied SQL or used to bypass ownership/consent.
	NativeProjection func(context.Context, string, Review, atproto.Reference) ([]turso.Statement, bool, error)
	WorkLimit        int
	DB               Database
	Publisher        atproto.Publisher
	Records          atproto.Records
	Hydrate          func(context.Context, string, string) error
}
type manifest struct {
	Type      string              `json:"$type"`
	ProjectID string              `json:"projectId"`
	Digest    string              `json:"digest"`
	Refs      []atproto.Reference `json:"refs"`
}
type encodedEntity struct {
	Entry      Entry
	Record     json.RawMessage
	Key        string
	Collection string
}

func statement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (e Engine) owner(ctx context.Context, account, owner string) error {
	if e.DB == nil || account == "" || owner == "" {
		return errors.New("linked account and database are required")
	}
	r, err := e.DB.Execute(ctx, statement("SELECT 1 FROM account_atproto_identities WHERE account_id=? AND did=?", turso.Text(account), turso.Text(owner)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return errors.New("ATProto record owner does not match account")
	}
	return nil
}
func (e Engine) Inventory(ctx context.Context, account, owner, project string) (Review, error) {
	if err := e.owner(ctx, account, owner); err != nil {
		return Review{}, err
	}
	r, err := e.DB.Execute(ctx, statement("SELECT id FROM projects WHERE id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return Review{}, err
	}
	if len(r.Rows) != 1 {
		return Review{}, errors.New("project is not owned by account")
	}
	review := Review{OwnerDID: owner, ProjectID: project, Entries: []Entry{}, Warnings: []string{"Records are public and replicated; deleting them cannot recall previously replicated copies.", "Review repository identities, metadata and feedback for private source information before publishing."}}
	for _, d := range definitions {
		columns := []string{d.IDColumn()}
		for _, f := range d.Fields {
			column := f.Name
			if f.Name == "canonical_json" {
				column = "CASE WHEN canonical_json IS NULL THEN NULL WHEN json_valid(canonical_json) THEN json_remove(canonical_json,'$.instructions') ELSE canonical_json END"
			}
			columns = append(columns, column)
		}
		lastID := ""
		pageSize := 1000
		for {
			r, err = e.DB.Execute(ctx, statement("SELECT "+strings.Join(columns, ",")+" FROM "+d.Table+" WHERE ("+d.Selector+") AND "+d.IDColumn()+">? ORDER BY "+d.IDColumn()+" LIMIT ?", turso.Text(project), turso.Text(lastID), turso.Integer(int64(pageSize))))
			if err != nil {
				if pageSize > 1 && strings.Contains(err.Error(), "response exceeds") {
					pageSize = max(1, pageSize/2)
					continue
				}
				return Review{}, err
			}

			for _, row := range r.Rows {
				if len(row) != len(columns) {
					return Review{}, errors.New("invalid public inventory row")
				}
				id, ok := row[0].Value.(string)
				if !ok || id == "" {
					return Review{}, errors.New("invalid legacy identity")
				}
				entry := Entry{Table: d.Table, ID: id, Values: map[string]turso.Value{}}
				for i, f := range d.Fields {
					if row[i+1].Type != "null" {
						entry.Values[f.Name] = row[i+1]
					}
				}
				review.Entries = append(review.Entries, entry)
				review.HasTelemetry = review.HasTelemetry || hasTelemetry(entry, d)
				if len(review.Entries) > MaxEntries {
					return Review{}, errors.New("project inventory exceeds bounded record limit")
				}
			}
			if len(r.Rows) < pageSize {
				break
			}
			lastID = fmt.Sprint(r.Rows[len(r.Rows)-1][0].Value)
		}
	}

	return finalize(review)
}
func finalize(r Review) (Review, error) {
	sort.Slice(r.Entries, func(i, j int) bool {
		if r.Entries[i].Table == r.Entries[j].Table {
			return r.Entries[i].ID < r.Entries[j].ID
		}
		return r.Entries[i].Table < r.Entries[j].Table
	})
	if err := validateReview(r); err != nil {
		return Review{}, err
	}
	records := []json.RawMessage{}
	for i, entry := range r.Entries {
		if i > 0 && r.Entries[i-1].Table == entry.Table && r.Entries[i-1].ID == entry.ID {
			return Review{}, errors.New("duplicate public entity")
		}
		encoded, err := encode(r.ProjectID, entry)
		if err != nil {
			return Review{}, err
		}
		records = append(records, encoded.Record)
	}
	raw, err := json.Marshal(struct {
		Owner, Project string
		Records        []json.RawMessage
	}{r.OwnerDID, r.ProjectID, records})
	if err != nil {
		return Review{}, err
	}
	r.Digest = digest(raw)
	return r, nil
}
func encode(project string, entry Entry) (encodedEntity, error) {
	d, err := definition(entry.Table)
	if err != nil {
		return encodedEntity{}, err
	}
	if entry.ID == "" {
		return encodedEntity{}, errors.New("legacy ID is required")
	}
	values := map[string]any{"$type": d.Collection, "projectId": project, "legacyId": entry.ID}
	allowed := map[string]Field{}
	for _, f := range d.Fields {
		allowed[f.Name] = f
	}
	for name, value := range entry.Values {
		f, ok := allowed[name]
		if !ok {
			return encodedEntity{}, fmt.Errorf("%s is not a public %s field", name, entry.Table)
		}
		v, err := fieldValue(f, value)
		if err != nil {
			return encodedEntity{}, err
		}
		if v != nil {
			values[name] = v
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return encodedEntity{}, err
	}
	if len(raw) > 256<<10 {
		return encodedEntity{}, errors.New("domain record exceeds 256KiB")
	}
	return encodedEntity{entry, raw, atproto.StableKey(d.Collection, project+"\x00"+entry.ID+"\x00"+digest(raw)), d.Collection}, nil
}
func placeholder(owner, collection, key string) atproto.Mutation {
	return atproto.Mutation{Repo: owner, Collection: collection, RKey: key, Record: json.RawMessage(`{"$type":"` + collection + `"}`)}
}
func (e Engine) readManifest(ctx context.Context, owner, project string) (manifest, atproto.Reference, error) {
	if e.Records == nil {
		return manifest{}, atproto.Reference{}, errors.New("canonical records client is required")
	}
	m := placeholder(owner, Namespace+"manifest", atproto.StableKey(Namespace+"manifest", project))
	r, err := e.Records.Get(ctx, m)
	if err != nil {
		return manifest{}, atproto.Reference{}, err
	}
	var value manifest
	if err = json.Unmarshal(r.Value, &value); err != nil || value.Type != m.Collection || value.ProjectID != project || len(value.Refs) > 1000 || value.Digest == "" {
		return manifest{}, atproto.Reference{}, fmt.Errorf("%w: invalid project manifest", ErrInvalidCanonical)
	}
	return value, r.Reference, nil
}

// ManifestInfo reads the authoritative remote pointer without reconciling or
// mutating the local projection. Authorization is supplied by the bound client.
func (e Engine) ManifestInfo(ctx context.Context, owner, project string) (atproto.Reference, string, error) {
	m, ref, err := e.readManifest(ctx, owner, project)
	return ref, m.Digest, err
}

// Publish is the explicit migration entry point. A changed inventory always
// invalidates consent; no data is made public merely by linking an account.
func (e Engine) Publish(ctx context.Context, account string, review Review, consent Consent) (atproto.Reference, error) {
	if !consent.Public || consent.ReviewDigest != review.Digest || review.HasTelemetry && !consent.Telemetry {
		return atproto.Reference{}, ErrConsent
	}
	current, err := e.PublicationReview(ctx, account, review.OwnerDID, review.ProjectID, consent.Telemetry)
	if err != nil {
		return atproto.Reference{}, err
	}
	if current.Digest != review.Digest {
		return atproto.Reference{}, ErrChanged
	}
	if current.HasTelemetry && !consent.Telemetry {
		return atproto.Reference{}, ErrConsent
	}
	existing, prior, err := e.readManifest(ctx, review.OwnerDID, review.ProjectID)
	if err != nil && !errors.Is(err, atproto.ErrNotFound) {
		return atproto.Reference{}, err
	}
	if err == nil && existing.Digest == current.Digest {
		if err = e.project(ctx, account, current, consent, prior); err != nil {
			return atproto.Reference{}, err
		}
		return prior, nil
	}
	return e.publish(ctx, account, current, consent, prior, func() error {
		latest, err := e.PublicationReview(ctx, account, review.OwnerDID, review.ProjectID, consent.Telemetry)
		if err != nil {
			return err
		}
		if latest.Digest != review.Digest {
			return ErrChanged
		}
		return nil
	})
}
func (e Engine) publish(ctx context.Context, account string, review Review, consent Consent, prior atproto.Reference, check func() error) (atproto.Reference, error) {
	if e.BeforePublish != nil {
		if err := e.BeforePublish(ctx, review); err != nil {
			return atproto.Reference{}, err
		}
	}
	p, err := e.progress(ctx, review, prior)
	if err != nil {
		return atproto.Reference{}, err
	}
	cache, err := e.versionCache(ctx, review)
	if err != nil {
		return atproto.Reference{}, err
	}
	limit := e.WorkLimit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	used := 0
	pending := func() error {
		if err := e.saveProgress(ctx, review, &p); err != nil {
			return err
		}
		return &PendingError{Completed: len(p.Entries) + len(p.Shards), Total: len(review.Entries) + (len(review.Entries)+99)/100 + 1, Digest: review.Digest}
	}
	for i := len(p.Entries); i < len(review.Entries); i++ {
		item, err := encode(review.ProjectID, review.Entries[i])
		if err != nil {
			return atproto.Reference{}, err
		}
		ref, found := cache[item.Collection+"/"+item.Key]
		if !found {
			if used >= limit {
				return atproto.Reference{}, pending()
			}
			ref, err = e.Publisher.Publish(ctx, review.OwnerDID, "entity:"+item.Key, atproto.Mutation{Repo: review.OwnerDID, Collection: item.Collection, RKey: item.Key, Record: item.Record})
			if err != nil {
				return atproto.Reference{}, err
			}
			used++
			if err = e.cacheVersion(ctx, review, item.Collection, item.Key, ref); err != nil {
				return atproto.Reference{}, err
			}
		}
		p.Entries = append(p.Entries, ref)
		if !found || len(p.Entries)-p.SavedEntries >= 50 {
			if err = e.saveProgress(ctx, review, &p); err != nil {
				return atproto.Reference{}, err
			}
		}
	}
	if err = e.saveProgress(ctx, review, &p); err != nil {
		return atproto.Reference{}, err
	}
	for index := len(p.Shards); index < (len(p.Entries)+99)/100; index++ {
		start := index * 100
		end := min(start+100, len(p.Entries))
		entryRefs, err := e.progressRefs(ctx, p.RunID, "entry", start, end)
		if err != nil {
			return atproto.Reference{}, err
		}
		value := manifest{Type: Namespace + "manifestShard", ProjectID: review.ProjectID, Digest: refDigest(entryRefs), Refs: entryRefs}
		raw, _ := json.Marshal(value)
		key := atproto.StableKey(value.Type, digest(raw))
		ref, found := cache[value.Type+"/"+key]
		if !found {
			if used >= limit {
				return atproto.Reference{}, pending()
			}
			ref, err = e.Publisher.Publish(ctx, review.OwnerDID, "shard:"+key, atproto.Mutation{Repo: review.OwnerDID, Collection: value.Type, RKey: key, Record: raw})
			if err != nil {
				return atproto.Reference{}, err
			}
			used++
			if err = e.cacheVersion(ctx, review, value.Type, key, ref); err != nil {
				return atproto.Reference{}, err
			}
		}
		p.Shards = append(p.Shards, ref)
		if !found || len(p.Shards)-p.SavedShards >= 50 {
			if err = e.saveProgress(ctx, review, &p); err != nil {
				return atproto.Reference{}, err
			}
		}
	}
	if used >= limit {
		return atproto.Reference{}, pending()
	}
	if err = e.saveProgress(ctx, review, &p); err != nil {
		return atproto.Reference{}, err
	}
	if check != nil {
		if err = check(); err != nil {
			return atproto.Reference{}, err
		}
	}
	value := manifest{Type: Namespace + "manifest", ProjectID: review.ProjectID, Digest: review.Digest, Refs: p.Shards}
	raw, _ := json.Marshal(value)
	mutation := placeholder(review.OwnerDID, value.Type, atproto.StableKey(value.Type, review.ProjectID))
	mutation.Record = raw
	if p.Prior.CID != "" {
		mutation.ExpectedCID = &p.Prior.CID
	}
	ref, err := e.Publisher.Publish(ctx, review.OwnerDID, "manifest:"+p.RunID, mutation)
	if err != nil {
		return atproto.Reference{}, err
	}
	if err = e.project(ctx, account, review, consent, ref); err != nil {
		return atproto.Reference{}, err
	}
	return ref, nil
}

func (e Engine) canonicalReview(ctx context.Context, owner, project string) (Review, atproto.Reference, error) {
	manifest, ref, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return Review{}, atproto.Reference{}, err
	}
	review, err := e.rebuildReview(ctx, owner, project, manifest, ref)
	return review, ref, err
}

func (e Engine) fetch(ctx context.Context, owner string, ref atproto.Reference, out any) error {
	prefix := "at://" + owner + "/"
	if !strings.HasPrefix(ref.URI, prefix) {
		return fmt.Errorf("%w: cross-owner canonical reference", ErrInvalidCanonical)
	}
	parts := strings.Split(strings.TrimPrefix(ref.URI, prefix), "/")
	if len(parts) != 2 {
		return fmt.Errorf("%w: invalid canonical URI", ErrInvalidCanonical)
	}
	r, err := e.Records.Get(ctx, placeholder(owner, parts[0], parts[1]))
	if err != nil {
		return err
	}
	if r.URI != ref.URI || r.CID != ref.CID {
		return atproto.ErrConflict
	}
	if err := json.Unmarshal(r.Value, out); err != nil {
		return fmt.Errorf("%w: invalid reference value", ErrInvalidCanonical)
	}
	return nil
}
func decodeValue(f Field, raw []byte) (turso.Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var v any
	if decoder.Decode(&v) != nil || decoder.Decode(new(any)) != io.EOF {
		return turso.Value{}, errors.New("invalid canonical field")
	}
	switch f.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return turso.Value{}, errors.New("invalid canonical string")
		}
		return turso.Text(s), nil
	case "decimal":
		s, ok := v.(string)
		if !ok {
			return turso.Value{}, errors.New("invalid canonical decimal")
		}
		cell := turso.Value{Type: "float", Value: s}
		if _, err := fieldValue(f, cell); err != nil {
			return turso.Value{}, err
		}
		return cell, nil
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return turso.Value{}, errors.New("invalid canonical integer")
		}
		i, err := n.Int64()
		if err != nil {
			return turso.Value{}, err
		}
		return turso.Integer(i), nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return turso.Value{}, errors.New("invalid canonical boolean")
		}
		if b {
			return turso.Integer(1), nil
		}
		return turso.Integer(0), nil
	}
	return turso.Value{}, errors.New("unknown canonical type")
}

func (e Engine) Reconcile(ctx context.Context, account, owner, project string) error {
	if err := e.owner(ctx, account, owner); err != nil {
		return err
	}
	consent, local, err := e.state(ctx, account, project)
	if err != nil {
		return err
	}
	_, remote, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return err
	}
	if local.CID == remote.CID {
		return e.finishActivation(ctx, account, project, remote)
	}
	review, ref, err := e.canonicalReview(ctx, owner, project)
	if err != nil {
		return err
	}
	consent, _, err = e.state(ctx, account, project)
	if err != nil {
		return err
	}
	if !consent.Public || review.HasTelemetry && !consent.Telemetry {
		return ErrConsent
	}
	return e.project(ctx, account, review, consent, ref)
}
func (e Engine) Update(ctx context.Context, account, owner, project string, changes []Change) (atproto.Reference, error) {
	if blocked, err := e.AuditBlocked(ctx, account, project); err != nil {
		return atproto.Reference{}, err
	} else if blocked {
		return atproto.Reference{}, ErrAuditInvalid
	}
	if err := e.owner(ctx, account, owner); err != nil {
		return atproto.Reference{}, err
	}
	consent, local, err := e.state(ctx, account, project)
	if err != nil {
		return atproto.Reference{}, err
	}
	if !consent.Public {
		return atproto.Reference{}, ErrNotMigrated
	}
	remote, prior, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return atproto.Reference{}, err
	}
	if e.ExpectedPriorCID != "" && prior.CID != e.ExpectedPriorCID {
		if e.ExpectedDigest == "" || remote.Digest != e.ExpectedDigest {
			return atproto.Reference{}, atproto.ErrConflict
		}
		proof, err := e.DB.Execute(ctx, statement("SELECT 1 FROM domain_publication_runs WHERE project_id=? AND owner_did=? AND digest=? AND prior_cid=?", turso.Text(project), turso.Text(owner), turso.Text(e.ExpectedDigest), turso.Text(e.ExpectedPriorCID)))
		if err != nil {
			return atproto.Reference{}, err
		}
		if len(proof.Rows) != 1 {
			return atproto.Reference{}, atproto.ErrConflict
		}
		if local.CID == prior.CID {
			return prior, e.finishActivation(ctx, account, project, prior)
		}
		if local.CID != e.ExpectedPriorCID {
			return atproto.Reference{}, atproto.ErrConflict
		}
		base, err := e.PublicationReview(ctx, account, owner, project, consent.Telemetry)
		if err != nil {
			return atproto.Reference{}, err
		}
		resumed, err := ApplyChanges(base, changes, consent.Telemetry)
		if err != nil {
			return atproto.Reference{}, err
		}
		if resumed.Digest != e.ExpectedDigest {
			return atproto.Reference{}, ErrChanged
		}
		progress, err := e.progress(ctx, resumed, local)
		if err != nil {
			return atproto.Reference{}, err
		}
		if len(progress.Entries) != len(resumed.Entries) || len(progress.Shards) != (len(resumed.Entries)+99)/100 {
			return atproto.Reference{}, atproto.ErrConflict
		}
		value := manifest{Type: Namespace + "manifest", ProjectID: project, Digest: resumed.Digest, Refs: progress.Shards}
		raw, _ := json.Marshal(value)
		mutation := placeholder(owner, value.Type, atproto.StableKey(value.Type, project))
		mutation.Record = raw
		mutation.ExpectedCID = &progress.Prior.CID
		confirmed, err := e.Publisher.Publish(ctx, owner, "manifest:"+progress.RunID, mutation)
		if err != nil {
			return atproto.Reference{}, err
		}
		if confirmed.CID != prior.CID {
			return atproto.Reference{}, atproto.ErrConflict
		}
		if err = e.project(ctx, account, resumed, consent, confirmed); err != nil {
			return atproto.Reference{}, err
		}
		return confirmed, nil
	}
	var review Review
	if local.CID == prior.CID {
		if err = e.finishActivation(ctx, account, project, prior); err != nil {
			return atproto.Reference{}, err
		}
		review, err = e.PublicationReview(ctx, account, owner, project, consent.Telemetry)
		if err == nil && review.Digest != remote.Digest {
			err = errors.New("local canonical projection drift requires explicit reconcile")
		}
	} else {
		review, prior, err = e.canonicalReview(ctx, owner, project)
		if err == nil {
			err = e.project(ctx, account, review, consent, prior)
		}
	}
	if err != nil {
		return atproto.Reference{}, err
	}

	review, err = ApplyChanges(review, changes, consent.Telemetry)
	if err != nil {
		return atproto.Reference{}, err
	}

	if e.ExpectedDigest != "" && review.Digest != e.ExpectedDigest {
		return atproto.Reference{}, ErrChanged
	}
	if review.Digest == remote.Digest {
		// A native command may update private authorization state or return rows
		// without changing any public record. Its durable SQL receipt must still
		// complete, bound to the unchanged canonical manifest.
		if e.NativeProjection != nil {
			if e.BeforePublish != nil {
				if err := e.BeforePublish(ctx, review); err != nil {
					return atproto.Reference{}, err
				}
			}
			if err := e.project(ctx, account, review, consent, prior); err != nil {
				return atproto.Reference{}, err
			}
		}
		return prior, nil
	}
	return e.publish(ctx, account, review, consent, prior, nil)
}

// ApplyChanges is a pure preview of reviewed changes, used to bind durable native
// intents to the precise canonical target digest before any public writes.
func ApplyChanges(review Review, changes []Change, telemetryConsent bool) (Review, error) {
	entries := map[string]Entry{}
	for _, entry := range review.Entries {
		clone := make(map[string]turso.Value, len(entry.Values))
		for k, v := range entry.Values {
			clone[k] = v
		}
		entry.Values = clone
		entries[entry.Table+"\x00"+entry.ID] = entry
	}
	for _, change := range changes {
		d, err := definition(change.Table)
		if err != nil {
			return Review{}, err
		}
		if change.Table == "projects" && change.ID != review.ProjectID {
			return Review{}, errors.New("cross-project change")
		}
		if change.ID == "" || change.Delete && change.Table == "projects" {
			return Review{}, errors.New("project identity cannot be removed")
		}
		key := change.Table + "\x00" + change.ID
		if change.Delete {
			delete(entries, key)
			continue
		}
		entry, ok := entries[key]
		if !ok {
			entry = Entry{Table: change.Table, ID: change.ID, Values: map[string]turso.Value{}}
		}
		for field, value := range change.Values {
			if value.Type == "null" {
				delete(entry.Values, field)
			} else {
				entry.Values[field] = value
			}
		}
		if change.Table == "project_runtime_settings" && !telemetryConsent {
			if v, ok := entry.Values["telemetry_enabled"]; ok && fmt.Sprint(v.Value) == "1" {
				return Review{}, ErrConsent
			}
		}
		if _, err := encode(review.ProjectID, entry); err != nil {
			return Review{}, err
		}
		entries[key] = entry
		if d.Telemetry && !telemetryConsent {
			return Review{}, ErrConsent
		}
	}
	review.Entries = nil
	review.HasTelemetry = false
	for _, entry := range entries {
		d, _ := definition(entry.Table)
		review.HasTelemetry = review.HasTelemetry || hasTelemetry(entry, d)
		review.Entries = append(review.Entries, entry)
	}
	return finalize(review)
}

// Import rebuilds a missing local project from its owner-authorized manifest.
// The caller reviews its public digest and grants publication/telemetry consent
// explicitly; this is not triggered by login or ordinary account linking.
func (e Engine) Import(ctx context.Context, account, owner, project string, consent Consent) error {
	if err := e.owner(ctx, account, owner); err != nil {
		return err
	}
	m, ref, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return err
	}
	if !consent.Public || consent.ReviewDigest != m.Digest {
		return ErrConsent
	}
	review, err := e.rebuildReview(ctx, owner, project, m, ref)
	if err != nil {
		return err
	}
	if review.HasTelemetry && !consent.Telemetry {
		return ErrConsent
	}
	return e.project(ctx, account, review, consent, ref)
}
