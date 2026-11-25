package lexiconpublish

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
)

type memoryRecords struct {
	records             map[string]atproto.Record
	writes              int
	loseAcknowledgement bool
}

func (m *memoryRecords) Get(_ context.Context, mutation atproto.Mutation) (atproto.Record, error) {
	record, ok := m.records[mutation.RKey]
	if !ok {
		return atproto.Record{}, atproto.ErrNotFound
	}
	return record, nil
}
func (m *memoryRecords) Put(_ context.Context, mutation atproto.Mutation) (atproto.Reference, error) {
	current, exists := m.records[mutation.RKey]
	if exists && (mutation.ExpectedCID == nil || *mutation.ExpectedCID != current.CID) || !exists && mutation.ExpectedCID != nil {
		return atproto.Reference{}, atproto.ErrConflict
	}
	cid, err := RecordCID(mutation.Record)
	if err != nil {
		return atproto.Reference{}, err
	}
	reference := atproto.Reference{URI: mutation.URI(), CID: cid}
	m.records[mutation.RKey] = atproto.Record{Reference: reference, Value: append([]byte(nil), mutation.Record...)}
	m.writes++
	if m.loseAcknowledgement {
		m.loseAcknowledgement = false
		return atproto.Reference{}, errors.New("lost PDS acknowledgement")
	}
	return reference, nil
}

type dnsValues map[string][]string

func (d dnsValues) LookupTXT(_ context.Context, name string) ([]string, error) { return d[name], nil }

func fixtureSchema(name string) map[string]any {
	return map[string]any{"lexicon": 1, "id": "dev.mycontextprotocol.test." + name, "defs": map[string]any{"main": map[string]any{"type": "record", "key": "any", "record": map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}}
}
func writeSchema(t *testing.T, directory string, doc map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(directory, doc["id"].(string)+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func loadFixture(t *testing.T) ([]Schema, string) {
	t.Helper()
	directory := t.TempDir()
	writeSchema(t, directory, fixtureSchema("one"))
	schemas, err := LoadDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	return schemas, directory
}

func TestPublishRecoveryAndIndependentPublicVerification(t *testing.T) {
	ctx := context.Background()
	schemas, _ := loadFixture(t)
	records := &memoryRecords{records: map[string]atproto.Record{}, loseAcknowledgement: true}
	journal := atproto.SQLJournal{DB: sqlitetest.New(t, atproto.MigrationSQL)}
	if _, err := Publish(ctx, records, journal, Authority, schemas); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	receipts, err := Publish(ctx, records, journal, Authority, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Verified || records.writes != 1 {
		t.Fatalf("retry receipts=%+v writes=%d", receipts, records.writes)
	}
	pending, err := journal.Pending(ctx, Authority, "lexicon:", 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending intent not reconciled: %v %+v", err, pending)
	}
	manifest, _ := DNSManifest(Authority, schemas)
	dns := dnsValues{}
	for _, entry := range manifest {
		dns[entry.Name] = []string{entry.Value}
	}
	receipts, err = Verify(ctx, records, dns, Authority, schemas)
	if err != nil || !receipts[0].Verified {
		t.Fatalf("public proof: %v %+v", err, receipts)
	}
	dns[manifest[0].Name] = []string{"did=did:plc:wrong"}
	if _, err := Verify(ctx, records, dns, Authority, schemas); err == nil {
		t.Fatal("wrong DNS authority accepted")
	}
	dns[manifest[0].Name] = []string{manifest[0].Value}
	record := records.records[schemas[0].NSID]
	record.CID = "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	records.records[schemas[0].NSID] = record
	if _, err := Verify(ctx, records, dns, Authority, schemas); err == nil {
		t.Fatal("forged schema CID accepted")
	}
}

func TestBreakingSchemasAreRejectedBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	one, two := fixtureSchema("one"), fixtureSchema("two")
	writeSchema(t, directory, one)
	writeSchema(t, directory, two)
	original, err := LoadDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	records := &memoryRecords{records: map[string]atproto.Record{}}
	for _, schema := range original {
		m := mutation(Authority, schema)
		records.records[schema.NSID] = atproto.Record{Reference: atproto.Reference{URI: m.URI(), CID: schema.CID}, Value: schema.Record}
	}
	one["description"] = "Nonbreaking documentation update"
	writeSchema(t, directory, one)
	two["defs"].(map[string]any)["main"].(map[string]any)["record"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)["type"] = "integer"
	writeSchema(t, directory, two)
	changed, err := LoadDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	journal := atproto.SQLJournal{DB: sqlitetest.New(t, atproto.MigrationSQL)}
	if _, err := Publish(ctx, records, journal, Authority, changed); err == nil || !strings.Contains(err.Error(), "breaking") {
		t.Fatalf("breaking schema accepted: %v", err)
	}
	if records.writes != 0 {
		t.Fatal("published a partial schema set before checking compatibility")
	}
}

func TestCompatibilityAllowsOnlyOptionalAdditionsAndProse(t *testing.T) {
	old := []byte(`{"lexicon":1,"id":"dev.mycontextprotocol.test.example","defs":{"main":{"type":"object","required":["name"],"properties":{"name":{"type":"string","maxLength":20}}}}}`)
	valid := []byte(`{"lexicon":1,"id":"dev.mycontextprotocol.test.example","description":"Updated prose","defs":{"main":{"type":"object","required":["name"],"properties":{"name":{"type":"string","maxLength":20},"optional":{"type":"boolean"}}}}}`)
	if err := Compatible(old, valid); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{strings.Replace(string(old), "maxLength\":20", "maxLength\":21", 1), strings.Replace(string(old), "[\"name\"]", "[]", 1), strings.Replace(string(old), "\"string\"", "\"integer\"", 1)} {
		if err := Compatible(old, []byte(replacement)); err == nil {
			t.Fatal("breaking change accepted")
		}
	}
}

func TestDirectoryRejectsUnresolvedReferencesAndSymlinks(t *testing.T) {
	schemas, directory := loadFixture(t)
	manifest, err := DNSManifest(Authority, schemas)
	if err != nil || manifest[0].Name != "_lexicon.test.mycontextprotocol.dev" {
		t.Fatalf("DNS group: %v %+v", err, manifest)
	}
	doc := fixtureSchema("broken")
	doc["defs"].(map[string]any)["main"].(map[string]any)["record"].(map[string]any)["properties"].(map[string]any)["value"] = map[string]any{"type": "ref", "ref": "dev.mycontextprotocol.test.missing#view"}
	writeSchema(t, directory, doc)
	if _, err := LoadDirectory(directory); err == nil {
		t.Fatal("unresolved schema reference accepted")
	}
	links := t.TempDir()
	source := filepath.Join(directory, schemas[0].NSID+".json")
	if err := os.Symlink(source, filepath.Join(links, schemas[0].NSID+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(links); err == nil {
		t.Fatal("symlink schema accepted")
	}
}

func TestCompleteRepositorySchemasResolvePinnedOfficialDependencies(t *testing.T) {
	schemas, err := LoadDirectory("../../../lexicons")
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) < 80 {
		t.Fatalf("expected complete product/data/permission schemas, got %d", len(schemas))
	}
	if err = validateSchemaSet(schemas); err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if schema.NSID == "com.atproto.repo.strongRef" {
			t.Fatal("dependency accidentally selected for publication")
		}
	}
}
