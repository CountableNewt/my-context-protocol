package atidentity

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// FlowClient exposes the persisted state to the browser-binding layer. The
// pinned SDK's convenience StartAuthFlow ignores store errors, so use its
// exported discovery/PAR primitives and require persistence before redirecting.
type FlowClient struct{ *oauth.ClientApp }

func (c FlowClient) StartAuth(ctx context.Context, identifier string) (string, string, error) {
	if c.ClientApp == nil {
		return "", "", errors.New("ATProto OAuth client is not configured")
	}
	id, err := syntax.ParseAtIdentifier(identifier)
	if err != nil {
		return "", "", err
	}
	ident, err := c.Dir.Lookup(ctx, id)
	if err != nil {
		return "", "", err
	}
	pds := ident.PDSEndpoint()
	if pds == "" {
		return "", "", errors.New("identity has no PDS")
	}
	server, err := c.Resolver.ResolveAuthServerURL(ctx, pds)
	if err != nil {
		return "", "", err
	}
	metadata, err := c.Resolver.ResolveAuthServerMetadata(ctx, server)
	if err != nil {
		return "", "", err
	}
	info, err := c.SendAuthRequest(ctx, metadata, c.Config.Scopes, identifier)
	if err != nil {
		return "", "", err
	}
	info.AccountDID = &ident.DID
	if err = c.Store.SaveAuthRequestInfo(ctx, *info); err != nil {
		return "", "", fmt.Errorf("persist authorization request: %w", err)
	}
	redirect := metadata.AuthorizationEndpoint + "?" + url.Values{"client_id": {c.Config.ClientID}, "request_uri": {info.RequestURI}}.Encode()
	return redirect, info.State, nil
}
