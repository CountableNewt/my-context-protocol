package session

import (
	"reflect"
	"testing"
)

func TestSwiftPayloadCompatibility(t *testing.T) {
	expected := Payload{"account_id": "cfcd2084-95d5-45ef-ae17-f565b24ce374", "github_access_token": "value", "oauth_state": "abc"}
	payload, err := Decode(`{"oauth_state":"abc","github_access_token":"value","account_id":"cfcd2084-95d5-45ef-ae17-f565b24ce374"}`)
	if err != nil || !reflect.DeepEqual(payload, expected) {
		t.Fatalf("decode failed: %v", err)
	}
	encoded, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(again, expected) {
		t.Fatalf("roundtrip failed: %v", err)
	}
	empty, err := Encode(nil)
	if err != nil || empty != "{}" {
		t.Fatalf("nil payload: %s %v", empty, err)
	}
}
func TestRejectNonStringPayloads(t *testing.T) {
	for _, payload := range []string{`null`, `[]`, `{"account_id":null}`, `{"account_id":42}`, `{"account_id":true}`, `{"account_id":{}}`, `bad`, string([]byte{'{', '"', 'a', '"', ':', '"', 255, '"', '}'})} {
		if _, err := Decode(payload); err == nil {
			t.Errorf("accepted %q", payload)
		}
	}
}
