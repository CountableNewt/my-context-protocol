package lexiconpublish

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util/ssrf"
)

type publicAuthorization struct{}

func (publicAuthorization) Authorize(context.Context, *http.Request, string) error { return nil }

// PublicRecords resolves the selected DID and reads its PDS without sending an
// app session or OAuth token. Both resolution and PDS reads reject redirects,
// environment proxies and private socket addresses.
func PublicRecords(ctx context.Context, authority string) (atproto.Records, error) {
	if authority != Authority {
		return nil, errors.New("unexpected Lexicon authority")
	}
	did, err := syntax.ParseDID(authority)
	if err != nil {
		return nil, err
	}
	transport := ssrf.PublicOnlyTransport()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 10 * time.Second
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("public identity redirects are disabled")
	}}
	directory := &identity.BaseDirectory{PLCURL: "https://plc.directory", HTTPClient: *client, PLCClient: client, UserAgent: "MyContextProtocol-LexiconVerifier"}
	ident, err := directory.LookupDID(ctx, did)
	if err != nil {
		return nil, err
	}
	if ident == nil || ident.DID != did || ident.PDSEndpoint() == "" {
		return nil, errors.New("authority DID has no verified PDS")
	}
	return atproto.NewClient(ident.PDSEndpoint(), nil, publicAuthorization{})
}
