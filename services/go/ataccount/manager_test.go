package ataccount

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	sdkidentity "github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

type linkedStub struct{ value *identity.ATProtoIdentity }

func (s linkedStub) ATProtoIdentity(context.Context, string) (*identity.ATProtoIdentity, error) {
	return s.value, nil
}

type sessionStub struct{ data oauth.ClientSessionData }

func (s sessionStub) ResumeSession(context.Context, syntax.DID, string) (*oauth.ClientSession, error) {
	data := s.data
	return &oauth.ClientSession{Data: &data}, nil
}

func managerFixture(t *testing.T) *Manager {
	t.Helper()
	did := syntax.DID("did:plc:aaaaaaaaaaaaaaaaaaaaaaaa")
	db := sqlitetest.New(t, MigrationSQL+atidentity.MigrationSQL)
	cipher, err := security.NewTokenCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	dir := sdkidentity.NewMockDirectory()
	dir.Insert(sdkidentity.Identity{DID: did, Services: map[string]sdkidentity.ServiceEndpoint{"atproto_pds": {Type: "AtprotoPersonalDataServer", URL: "https://pds.example.com"}}})
	return &Manager{DB: db, Identities: linkedStub{&identity.ATProtoIdentity{DID: did.String(), AccountID: "account", SessionID: "session"}}, Sessions: sessionStub{oauth.ClientSessionData{AccountDID: did, SessionID: "session", HostURL: "https://pds.example.com", AccessToken: "token", DPoPPrivateKeyMultibase: "key"}}, Store: &atidentity.Store{DB: db, Cipher: cipher}, Directory: dir}
}

func TestSessionLeaseSerializesProcessesAndReleasesOnFailure(t *testing.T) {
	m := managerFixture(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.WithSession(ctx, "account", func(context.Context, *oauth.ClientSession) error {
			close(entered)
			<-release
			return errors.New("operation failed")
		})
	}()
	<-entered
	if err := m.WithSession(ctx, "account", func(context.Context, *oauth.ClientSession) error {
		t.Error("concurrent session refresh allowed")
		return nil
	}); !errors.Is(err, ErrBusy) {
		t.Fatalf("lease: %v", err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("operation error lost")
	}
	if err := m.WithSession(ctx, "account", func(context.Context, *oauth.ClientSession) error { return nil }); err != nil {
		t.Fatal("lease was not released", err)
	}
}

func TestPersistenceFailureAndPDSMigrationFailClosed(t *testing.T) {
	m := managerFixture(t)
	ctx := context.Background()
	if err := m.Store.DeleteSession(ctx, "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", "session"); err != nil {
		t.Fatal(err)
	}
	if err := m.WithSession(ctx, "account", func(ctx context.Context, s *oauth.ClientSession) error {
		s.PersistSessionCallback(ctx, s.Data)
		return nil
	}); err == nil {
		t.Fatal("SDK persistence failure reported success")
	}
	m.Sessions = sessionStub{oauth.ClientSessionData{AccountDID: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", SessionID: "session", HostURL: "https://old.example.com"}}
	if err := m.WithSession(ctx, "account", func(context.Context, *oauth.ClientSession) error { t.Error("old PDS credentials used"); return nil }); !errors.Is(err, ErrReconnect) {
		t.Fatalf("PDS move: %v", err)
	}
}
