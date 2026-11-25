package lexiconpublish

import (
	"encoding/json"
	"os"
	"testing"
)

// Keep this baseline as release history. Breaking contracts require new NSIDs;
// regenerating current contracts must not rewrite prior compatibility evidence.
func TestCheckedInContractCompatibility(t *testing.T) {
	raw, err := os.ReadFile("../../../lexicons/compatibility/initial-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]json.RawMessage
	if err = json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	current, err := LoadDirectory("../../../lexicons")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Schema{}
	for _, schema := range current {
		byName[schema.NSID] = schema
	}
	for name, previous := range baseline {
		next, ok := byName[name]
		if !ok {
			t.Errorf("removed contract %s", name)
			continue
		}
		if err := Compatible(previous, next.Record); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
