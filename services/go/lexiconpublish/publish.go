// Package lexiconpublish validates, publishes and independently verifies the
// product's public Lexicon authority. Writes alone are never publication proof.
package lexiconpublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/bluesky-social/indigo/atproto/atdata"
	"github.com/bluesky-social/indigo/atproto/lexicon"
	"github.com/bluesky-social/indigo/atproto/syntax"
	cidlib "github.com/ipfs/go-cid"
)

const Authority = "did:plc:qy5pluw2bsuq2x6albsgkvx3"
const Collection = "com.atproto.lexicon.schema"

type Schema struct {
	NSID   string          `json:"nsid"`
	Digest string          `json:"digest"`
	CID    string          `json:"cid"`
	Record json.RawMessage `json:"-"`
}

type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}
type Receipt struct {
	NSID     string `json:"nsid"`
	URI      string `json:"uri"`
	CID      string `json:"cid"`
	Action   string `json:"action"`
	Verified bool   `json:"verified"`
}
type DNS interface {
	LookupTXT(context.Context, string) ([]string, error)
}
type Journal interface {
	atproto.Journal
	Pending(context.Context, string, string, int) ([]atproto.PendingIntent, error)
}

func LoadDirectory(directory string) ([]Schema, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil, err
	}
	catalog, err := validationCatalog()
	if err != nil {
		return nil, err
	}
	schemas := []Schema{}
	documents := map[string]map[string]any{}
	for _, path := range paths {
		if filepath.Base(path) == "operations.json" {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New("Lexicon inputs must be bounded regular files")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var schema lexicon.SchemaFile
		if err = json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("%s: invalid schema JSON", filepath.Base(path))
		}
		if !strings.HasPrefix(schema.ID, "dev.mycontextprotocol.") || filepath.Base(path) != schema.ID+".json" {
			return nil, errors.New("schema file must match the product NSID")
		}
		if err = catalog.AddSchemaFile(schema); err != nil {
			return nil, fmt.Errorf("%s: %w", schema.ID, err)
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			return nil, errors.New("invalid Lexicon document")
		}
		doc["$type"] = Collection
		record, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		cid, err := RecordCID(record)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", schema.ID, err)
		}
		digest := sha256.Sum256(record)
		schemas = append(schemas, Schema{NSID: schema.ID, Digest: hex.EncodeToString(digest[:]), CID: cid, Record: record})
		documents[schema.ID] = doc
	}
	if len(schemas) == 0 || len(schemas) > 1000 {
		return nil, errors.New("expected between one and 1000 product Lexicons")
	}
	for nsid, doc := range documents {
		if err := checkReferences(catalog, doc, nsid); err != nil {
			return nil, fmt.Errorf("%s: %w", nsid, err)
		}
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].NSID < schemas[j].NSID })
	return schemas, nil
}

func checkReferences(catalog *lexicon.BaseCatalog, value any, owner string) error {
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "ref" {
			target, _ := v["ref"].(string)
			if strings.HasPrefix(target, "#") {
				target = owner + target
			}
			if _, err := catalog.Resolve(target); err != nil {
				return err
			}
		}
		if v["type"] == "union" {
			if values, ok := v["refs"].([]any); ok {
				for _, value := range values {
					target, _ := value.(string)
					if strings.HasPrefix(target, "#") {
						target = owner + target
					}
					if _, err := catalog.Resolve(target); err != nil {
						return err
					}
				}
			}
		}
		for _, child := range v {
			if err := checkReferences(catalog, child, owner); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := checkReferences(catalog, child, owner); err != nil {
				return err
			}
		}
	}
	return nil
}

func RecordCID(record []byte) (string, error) {
	value, err := atdata.UnmarshalJSON(record)
	if err != nil {
		return "", err
	}
	encoded, err := atdata.MarshalCBOR(value)
	if err != nil {
		return "", err
	}
	cid, err := (cidlib.Prefix{Version: 1, Codec: cidlib.DagCBOR, MhType: 0x12, MhLength: 32}).Sum(encoded)
	if err != nil {
		return "", err
	}
	return cid.String(), nil
}

func validateSchemaSet(schemas []Schema) error {
	if len(schemas) == 0 || len(schemas) > 1000 {
		return errors.New("expected between one and 1000 reviewed Lexicons")
	}
	catalog, err := validationCatalog()
	if err != nil {
		return err
	}
	documents := map[string]map[string]any{}
	for _, schema := range schemas {
		if !strings.HasPrefix(schema.NSID, "dev.mycontextprotocol.") {
			return errors.New("unexpected Lexicon namespace")
		}
		var file lexicon.SchemaFile
		if json.Unmarshal(schema.Record, &file) != nil || file.ID != schema.NSID || file.Type != Collection {
			return errors.New("Lexicon record identity does not match schema")
		}
		if err := catalog.AddSchemaFile(file); err != nil {
			return err
		}
		cid, err := RecordCID(schema.Record)
		if err != nil || cid != schema.CID {
			return errors.New("reviewed Lexicon CID does not match record")
		}
		digest := sha256.Sum256(schema.Record)
		if hex.EncodeToString(digest[:]) != schema.Digest {
			return errors.New("reviewed Lexicon digest does not match record")
		}
		var doc map[string]any
		if json.Unmarshal(schema.Record, &doc) != nil {
			return errors.New("invalid Lexicon record")
		}
		documents[schema.NSID] = doc
	}
	for owner, doc := range documents {
		if err := checkReferences(catalog, doc, owner); err != nil {
			return err
		}
	}
	return nil
}

func DNSManifest(authority string, schemas []Schema) ([]DNSRecord, error) {
	if _, err := syntax.ParseDID(authority); err != nil {
		return nil, err
	}
	groups := map[string]bool{}
	for _, schema := range schemas {
		parts := strings.Split(schema.NSID, ".")
		if len(parts) < 3 {
			return nil, errors.New("invalid schema NSID")
		}
		authorityParts := parts[:len(parts)-1]
		for i, j := 0, len(authorityParts)-1; i < j; i, j = i+1, j-1 {
			authorityParts[i], authorityParts[j] = authorityParts[j], authorityParts[i]
		}
		groups["_lexicon."+strings.Join(authorityParts, ".")] = true
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DNSRecord, 0, len(names))
	for _, name := range names {
		out = append(out, DNSRecord{Name: name, Type: "TXT", Value: "did=" + authority})
	}
	return out, nil
}

// Compatible is deliberately conservative: an existing definition, constraint,
// enum, union, type or required field cannot change. New definitions and optional
// object fields are permitted; prose descriptions may be revised.
func Compatible(previous, next []byte) error {
	var oldDoc, newDoc map[string]any
	if json.Unmarshal(previous, &oldDoc) != nil || json.Unmarshal(next, &newDoc) != nil {
		return errors.New("invalid schema compatibility input")
	}
	delete(oldDoc, "$type")
	delete(newDoc, "$type")
	return compatibleNode(oldDoc, newDoc, "schema")
}

func compatibleNode(previous, next any, path string) error {
	old, ok := previous.(map[string]any)
	if !ok {
		if !reflect.DeepEqual(previous, next) {
			return fmt.Errorf("breaking schema change at %s", path)
		}
		return nil
	}
	current, ok := next.(map[string]any)
	if !ok {
		return fmt.Errorf("breaking schema type at %s", path)
	}
	for key, value := range old {
		if key == "description" {
			continue
		}
		other, exists := current[key]
		if !exists {
			return fmt.Errorf("removed schema field %s.%s", path, key)
		}
		if key == "properties" || key == "defs" {
			oldFields, ok := value.(map[string]any)
			newFields, valid := other.(map[string]any)
			if !ok || !valid {
				return errors.New("invalid schema field map")
			}
			for name, field := range oldFields {
				nextField, exists := newFields[name]
				if !exists {
					return fmt.Errorf("removed definition %s.%s.%s", path, key, name)
				}
				if err := compatibleNode(field, nextField, path+"."+key+"."+name); err != nil {
					return err
				}
			}
			continue
		}
		if err := compatibleNode(value, other, path+"."+key); err != nil {
			return err
		}
	}
	for key := range current {
		if _, exists := old[key]; !exists && key != "description" && key != "properties" && key != "defs" {
			return fmt.Errorf("new constraint %s.%s requires a new Lexicon name", path, key)
		}
	}
	// Adding a required field is a breaking change even when properties gained it.
	if !reflect.DeepEqual(old["required"], current["required"]) {
		return fmt.Errorf("required fields changed at %s", path)
	}
	return nil
}

func mutation(authority string, schema Schema) atproto.Mutation {
	return atproto.Mutation{Repo: authority, Collection: Collection, RKey: schema.NSID, Record: schema.Record}
}
func sameRecord(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Publish validates every existing schema before the first write. Durable journal
// retries use their original CAS precondition, even after a lost acknowledgement.
func Publish(ctx context.Context, records atproto.Records, journal Journal, authority string, schemas []Schema) ([]Receipt, error) {
	if authority != Authority || records == nil || journal == nil {
		return nil, errors.New("selected authority, records and durable journal are required")
	}
	if err := validateSchemaSet(schemas); err != nil {
		return nil, err
	}
	prepared := map[string]atproto.Mutation{}
	existing := map[string]atproto.Record{}
	desired := map[string]Schema{}
	for _, schema := range schemas {
		m := mutation(authority, schema)
		record, err := records.Get(ctx, m)
		if err != nil && !errors.Is(err, atproto.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			if err := Compatible(record.Value, schema.Record); err != nil {
				return nil, fmt.Errorf("%s: %w", schema.NSID, err)
			}
			m.ExpectedCID = &record.CID
			existing[schema.NSID] = record
		}
		prepared[schema.NSID] = m
		desired[schema.NSID] = schema
	}
	publisher := atproto.Publisher{Records: records, Journal: journal}
	after := "lexicon:"
	for pages := 0; ; pages++ {
		if pages >= 100 {
			return nil, errors.New("too many unfinished Lexicon intents; reconcile a bounded subset")
		}
		pending, err := journal.Pending(ctx, authority, after, 100)
		if err != nil {
			return nil, err
		}
		if len(pending) == 0 {
			break
		}
		finished := false
		for _, intent := range pending {
			after = intent.Key
			if !strings.HasPrefix(intent.Key, "lexicon:") {
				finished = true
				break
			}
			schema, ok := desired[intent.Mutation.RKey]
			if !ok || intent.Mutation.Collection != Collection || !sameRecord(intent.Mutation.Record, schema.Record) {
				continue
			}
			if _, err = publisher.Publish(ctx, authority, intent.Key, intent.Mutation); err != nil {
				return nil, err
			}
		}
		if finished || len(pending) < 100 {
			break
		}
	}
	receipts := make([]Receipt, 0, len(schemas))
	for _, schema := range schemas {
		m := prepared[schema.NSID]
		action := "create"
		if record, ok := existing[schema.NSID]; ok {
			action = "update"
			if sameRecord(record.Value, schema.Record) {
				receipts = append(receipts, Receipt{schema.NSID, m.URI(), record.CID, "unchanged", false})
				continue
			}
		}
		key := "lexicon:" + schema.NSID + ":" + schema.Digest
		ref, err := publisher.Publish(ctx, authority, key, m)
		if err != nil {
			return receipts, err
		}
		if ref.CID != schema.CID {
			return receipts, errors.New("published Lexicon CID does not match canonical schema bytes")
		}
		receipts = append(receipts, Receipt{schema.NSID, ref.URI, ref.CID, action, false})
	}
	return receipts, nil
}

// Verify uses public records and authoritative DNS resolution independently of
// the writer. Its receipts assert exact DAG-CBOR CID and schema bytes, not just a
// successful authenticated putRecord response.
func Verify(ctx context.Context, records atproto.Records, dns DNS, authority string, schemas []Schema) ([]Receipt, error) {
	if authority != Authority || records == nil || dns == nil {
		return nil, errors.New("selected authority, public records and DNS resolver are required")
	}
	if err := validateSchemaSet(schemas); err != nil {
		return nil, err
	}
	changes, err := DNSManifest(authority, schemas)
	if err != nil {
		return nil, err
	}
	for _, change := range changes {
		values, err := dns.LookupTXT(ctx, change.Name)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %s", change.Name)
		}
		matched := false
		for _, value := range values {
			if strings.HasPrefix(value, "did=") {
				if value != change.Value {
					return nil, fmt.Errorf("wrong Lexicon authority for %s", change.Name)
				}
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("missing Lexicon authority for %s", change.Name)
		}
	}
	out := make([]Receipt, 0, len(schemas))
	for _, schema := range schemas {
		m := mutation(authority, schema)
		record, err := records.Get(ctx, m)
		if err != nil {
			return nil, err
		}
		if record.URI != m.URI() || record.CID != schema.CID || !sameRecord(record.Value, schema.Record) {
			return nil, fmt.Errorf("public schema or CID mismatch for %s", schema.NSID)
		}
		out = append(out, Receipt{schema.NSID, record.URI, record.CID, "verified", true})
	}
	return out, nil
}
