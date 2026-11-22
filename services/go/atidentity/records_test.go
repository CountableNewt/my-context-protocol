package atidentity

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func TestSessionRecordsRequiresValidIdentityAndPDS(t *testing.T) {
	if _, err := SessionRecords(nil); err == nil {
		t.Fatal("missing session accepted")
	}
	for _, host := range []string{"http://pds.example.com", "https://127.0.0.1", "https://pds.example.com/private", "https://pds.example.com?token=secret"} {
		s := &oauth.ClientSession{Data: &oauth.ClientSessionData{AccountDID: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", HostURL: host}}
		if _, err := SessionRecords(s); err == nil {
			t.Fatalf("invalid PDS accepted: %s", host)
		}
	}
}

func TestSessionExecutorRejectsIdentityAndDestinationChanges(t *testing.T) {
	origin, _ := url.Parse("https://pds.example.com")
	s := sessionExecutor{origin: origin, owner: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, test := range []struct{ repo, url string }{
		{"did:plc:bbbbbbbbbbbbbbbbbbbbbbbb", "https://pds.example.com/xrpc/com.atproto.repo.putRecord"},
		{s.owner, "https://other.example.com/xrpc/com.atproto.repo.putRecord"},
		{s.owner, "https://pds.example.com/xrpc/com.atproto.repo.deleteRecord"},
	} {
		r, _ := http.NewRequest("POST", test.url, nil)
		if _, err := s.DoWithAuth(context.Background(), r, test.repo, syntax.NSID("com.atproto.repo.putRecord")); err == nil {
			t.Fatal("changed owner or endpoint reached SDK")
		}
	}
}
