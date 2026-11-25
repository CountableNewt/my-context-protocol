package atidentity

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	c, err := security.NewTokenCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{DB: sqlitetest.New(t, MigrationSQL), Cipher: c, Now: func() time.Time { return time.Unix(1000, 0) }}
}

func TestRequestEncryptedCreateOnlyAndConsumedOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	info := oauth.AuthRequestData{State: "secret-state", PKCEVerifier: "secret-verifier", DPoPPrivateKeyMultibase: "secret-key"}
	if err := s.SaveAuthRequestInfo(ctx, info); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthRequestInfo(ctx, info); err == nil {
		t.Fatal("duplicate state replaced original")
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT state_hash,payload_encrypted FROM atproto_oauth_requests", WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Rows[0] {
		if strings.Contains(v.Value.(string), "secret-") {
			t.Fatal("plaintext secret persisted")
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.GetAuthRequestInfo(ctx, info.State); results <- err })
	}
	wg.Wait()
	close(results)
	success, missing := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrNotFound) {
			missing++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || missing != 1 {
		t.Fatalf("consumption success=%d missing=%d", success, missing)
	}
}

func TestExpiredRequestAndBoundedCleanup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SaveAuthRequestInfo(ctx, oauth.AuthRequestData{State: "state", PKCEVerifier: "verifier", DPoPPrivateKeyMultibase: "key"}); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Unix(1600, 0) }
	if _, err := s.GetAuthRequestInfo(ctx, "state"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	if err := s.PruneRequests(ctx, 0); err == nil {
		t.Fatal("unbounded cleanup accepted")
	}
	if err := s.PruneRequests(ctx, 1); err != nil {
		t.Fatal(err)
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT state_hash FROM atproto_oauth_requests", WantRows: true})
	if err != nil || len(r.Rows) != 0 {
		t.Fatalf("cleanup: %v %+v", err, r)
	}
}

func TestSessionOwnerIsolationAndDeletion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	did := syntax.DID("did:plc:aaaaaaaaaaaaaaaaaaaaaaaa")
	data := oauth.ClientSessionData{AccountDID: did, SessionID: "session", AccessToken: "access", RefreshToken: "refresh", DPoPPrivateKeyMultibase: "key"}
	if err := s.SaveSession(ctx, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, did, "session")
	if err != nil || got.RefreshToken != data.RefreshToken {
		t.Fatalf("roundtrip: %v %+v", err, got)
	}
	if _, err := s.GetSession(ctx, syntax.DID("did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"), "session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner: %v", err)
	}
	data.AccessToken = "rotated"
	if err := s.SaveSession(ctx, data); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSession(ctx, did, "session")
	if err != nil || got.AccessToken != "rotated" {
		t.Fatalf("refresh: %v %+v", err, got)
	}
	if err := s.DeleteSession(ctx, did, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, did, "session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := s.SaveSession(ctx, data); err == nil {
		t.Fatal("in-flight refresh recreated revoked session")
	}
}
