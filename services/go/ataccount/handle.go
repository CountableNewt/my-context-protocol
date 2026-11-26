package ataccount

import (
	"context"
	"errors"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
)

// ResolveHandle returns display metadata only. Indigo's LookupDID verifies that
// the DID's declared handle resolves back to that DID; never use AlsoKnownAs
// directly or replace a stored repository owner DID with this mutable label.
func (m *Manager) ResolveHandle(ctx context.Context, rawDID string) (string, error) {
	if m == nil || m.Directory == nil {
		return "", errors.New("ATProto identity directory is unavailable")
	}
	did, err := syntax.ParseDID(rawDID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ident, err := m.Directory.LookupDID(ctx, did)
	if err != nil {
		return "", err
	}
	if ident == nil || ident.DID != did || ident.Handle.IsInvalidHandle() {
		return "", errors.New("ATProto owner has no verified handle")
	}
	handle, err := syntax.ParseHandle(ident.Handle.String())
	if err != nil {
		return "", err
	}
	return handle.Normalize().String(), nil
}
