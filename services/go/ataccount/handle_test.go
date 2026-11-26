package ataccount

import (
	"context"
	sdkidentity "github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"testing"
)

type mismatchedDirectory struct{ sdkidentity.Directory }

func (d mismatchedDirectory) LookupDID(context.Context, syntax.DID) (*sdkidentity.Identity, error) {
	return &sdkidentity.Identity{DID: syntax.DID("did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"), Handle: syntax.Handle("other.example.com")}, nil
}
func TestResolveHandleUsesVerifiedIdentityWithoutChangingDID(t *testing.T) {
	did := syntax.DID("did:plc:aaaaaaaaaaaaaaaaaaaaaaaa")
	dir := sdkidentity.NewMockDirectory()
	dir.Insert(sdkidentity.Identity{DID: did, Handle: syntax.Handle("Owner.Example.com")})
	m := &Manager{Directory: dir}
	handle, err := m.ResolveHandle(context.Background(), did.String())
	if err != nil || handle != "owner.example.com" {
		t.Fatalf("handle=%q error=%v", handle, err)
	}
	for _, invalid := range []syntax.Handle{syntax.HandleInvalid, syntax.Handle(""), syntax.Handle("did:plc:not-a-handle")} {
		dir.Insert(sdkidentity.Identity{DID: did, Handle: invalid})
		if handle, err := m.ResolveHandle(context.Background(), did.String()); err == nil || handle != "" {
			t.Fatalf("unverified handle accepted %q %v", handle, err)
		}
	}
	m.Directory = mismatchedDirectory{Directory: dir}
	if handle, err := m.ResolveHandle(context.Background(), did.String()); err == nil || handle != "" {
		t.Fatalf("wrong DID accepted %q %v", handle, err)
	}
	m.Directory = dir
	if _, err := m.ResolveHandle(context.Background(), "did:plc:cccccccccccccccccccccccc"); err == nil {
		t.Fatal("missing identity accepted")
	}
}
