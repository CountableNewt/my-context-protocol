package xrpcauth

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func TestVerifierSignatureAudienceMethodExpiryAndReplay(t *testing.T) {
	ctx := context.Background()
	did := syntax.DID("did:plc:aaaaaaaaaaaaaaaaaaaaaaaa")
	method := syntax.NSID("dev.mycontextprotocol.project.createProject")
	key, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := key.PublicKey()
	dir := identity.NewMockDirectory()
	dir.Insert(identity.Identity{DID: did, Keys: map[string]identity.VerificationMethod{"atproto": {Type: "Multikey", PublicKeyMultibase: pub.Multibase()}}})
	db := sqlitetest.New(t, `CREATE TABLE accounts(id TEXT PRIMARY KEY,is_admin INTEGER NOT NULL);CREATE TABLE account_atproto_identities(did TEXT PRIMARY KEY,account_id TEXT);INSERT INTO accounts VALUES('account',1);INSERT INTO account_atproto_identities VALUES('did:plc:aaaaaaaaaaaaaaaaaaaaaaaa','account');`+MigrationSQL)
	v := Verifier{Audience: "did:web:mycontextprotocol.dev", Directory: dir, DB: db}
	for _, tc := range []struct {
		name, aud string
		method    *syntax.NSID
		ttl       time.Duration
	}{
		{"wrong audience", "did:web:other.example", &method, time.Minute},
		{"missing method", v.Audience, nil, time.Minute},
		{"long expiry", v.Audience, &method, 2 * time.Minute},
		{"expired", v.Audience, &method, -time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := auth.SignServiceAuth(did, tc.aud, tc.ttl, tc.method, key)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/xrpc/"+method.String(), nil)
			r.Header.Set("Authorization", "Bearer "+token)
			if _, err = v.Verify(ctx, r); err == nil {
				t.Fatal("invalid credential accepted")
			}
		})
	}
	token, err := auth.SignServiceAuth(did, v.Audience, time.Minute, &method, key)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/xrpc/"+method.String(), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, err := v.Verify(ctx, r)
	if err != nil || p.AccountID != "account" || !p.IsAdmin || !p.Scopes["rpc:"+method.String()] {
		t.Fatalf("valid token: %+v %v", p, err)
	}
	if _, err = v.Verify(ctx, r); err == nil {
		t.Fatal("mutation credential replay accepted")
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: `DELETE FROM account_atproto_identities`}); err != nil {
		t.Fatal(err)
	}
	token, _ = auth.SignServiceAuth(did, v.Audience, time.Minute, &method, key)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, err = v.Verify(ctx, r); err == nil {
		t.Fatal("unlinked DID accepted")
	}
}
