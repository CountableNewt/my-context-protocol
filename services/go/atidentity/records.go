package atidentity

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// SessionRecords binds publication to the resumed session's DID and resolved
// PDS. The SDK handles DPoP nonce rotation and token refresh for each request.
// Callers must serialize refresh of the same persisted session across processes.
func SessionRecords(session *oauth.ClientSession) (*atproto.Client, error) {
	if session == nil || session.Data == nil {
		return nil, errors.New("ATProto OAuth session is required")
	}
	if _, err := syntax.ParseDID(session.Data.AccountDID.String()); err != nil {
		return nil, err
	}
	pds, err := publicURL(session.Data.HostURL)
	if err != nil || pds.Path != "" && pds.Path != "/" || pds.RawQuery != "" {
		return nil, errors.New("session has invalid PDS origin")
	}
	// Do not inherit a caller's permissive transport for refresh requests.
	session.Client = publicHTTPClient()
	doer := &sessionExecutor{session: session, client: publicHTTPClient(), origin: pds, owner: session.Data.AccountDID.String()}
	return atproto.NewAuthenticatedClient(pds.String(), doer)
}

type sessionExecutor struct {
	session *oauth.ClientSession
	client  *http.Client
	origin  *url.URL
	owner   string
}

func (s *sessionExecutor) DoWithAuth(ctx context.Context, r *http.Request, repo string, nsid syntax.NSID) (*http.Response, error) {
	if repo != s.owner || r.URL.Scheme != s.origin.Scheme || r.URL.Host != s.origin.Host || r.URL.User != nil || r.URL.Fragment != "" || r.URL.Path != "/xrpc/"+nsid.String() {
		return nil, errors.New("ATProto request does not match session owner and PDS")
	}
	return s.session.DoWithAuth(s.client, r.WithContext(ctx), nsid)
}
