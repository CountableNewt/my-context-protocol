package xrpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bluesky-social/indigo/atproto/lexicon"
)

// Validate draft schemas with the protocol SDK as well as the inventory checker.
func TestLexiconsValidateWithProtocolSDK(t *testing.T) {
	paths, err := filepath.Glob("../../../lexicons/dev.mycontextprotocol.*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("schema inventory unavailable: %v", err)
	}
	catalog := lexicon.NewBaseCatalog()
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema lexicon.SchemaFile
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err = catalog.AddSchemaFile(schema); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, op := range Operations() {
		if _, err := catalog.Resolve(op.NSID); err != nil {
			t.Fatal(err)
		}
	}
}
