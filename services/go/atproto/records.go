// Package atproto provides canonical publication primitives. Authorization,
// consent, durable journals, and projection updates are supplied by callers.
package atproto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util/ssrf"
	cidlib "github.com/ipfs/go-cid"
)

var ErrConflict = errors.New("canonical record conflict")
var ErrNotFound = errors.New("canonical record not found")

// StableKey scopes a legacy identifier to a collection without exposing it.
func StableKey(collection, legacyID string) string {
	sum := sha256.Sum256([]byte(collection + "\x00" + legacyID))
	return hex.EncodeToString(sum[:])
}

type Reference struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

type Mutation struct {
	Repo       string          `json:"repo"`
	Collection string          `json:"collection"`
	RKey       string          `json:"rkey"`
	Record     json.RawMessage `json:"record"`
	// Nil means create only (swapRecord: null); updates require the prior CID.
	ExpectedCID *string `json:"swapRecord"`
}

func (m Mutation) Validate() error {
	_, didErr := syntax.ParseDID(m.Repo)
	_, nsidErr := syntax.ParseNSID(m.Collection)
	_, keyErr := syntax.ParseRecordKey(m.RKey)
	if didErr != nil || nsidErr != nil || keyErr != nil {
		return errors.New("invalid canonical record identity")
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(m.Record, &record); err != nil || record == nil {
		return errors.New("record must be a JSON object")
	}
	var kind string
	if json.Unmarshal(record["$type"], &kind) != nil || kind != m.Collection {
		return errors.New("record type must match collection")
	}
	if m.ExpectedCID != nil && !validCID(*m.ExpectedCID) {
		return errors.New("invalid expected CID")
	}
	return nil
}

func (m Mutation) URI() string { return "at://" + m.Repo + "/" + m.Collection + "/" + m.RKey }

type Record struct {
	Reference
	Value json.RawMessage `json:"value"`
}

type Records interface {
	Get(context.Context, Mutation) (Record, error)
	Put(context.Context, Mutation) (Reference, error)
}

// Authorizer attaches OAuth authorization and DPoP for this exact request.
// Implementations must verify that the session DID matches the supplied repo.
type Authorizer interface {
	Authorize(context.Context, *http.Request, string) error
}

type Client struct {
	endpoint *url.URL
	http     *http.Client
	auth     Authorizer
	doer     AuthenticatedDoer
}

// AuthenticatedDoer owns OAuth DPoP nonce retries and refresh. It must bind repo
// to its session DID, use a hardened HTTP client, and never follow redirects.
type AuthenticatedDoer interface {
	DoWithAuth(context.Context, *http.Request, string, syntax.NSID) (*http.Response, error)
}

func NewAuthenticatedClient(origin string, doer AuthenticatedDoer) (*Client, error) {
	if doer == nil {
		return nil, errors.New("authenticated executor required")
	}
	c, err := NewClient(origin, nil, executorAuthorizer{})
	if err != nil {
		return nil, err
	}
	c.doer = doer
	return c, nil
}

type executorAuthorizer struct{}

func (executorAuthorizer) Authorize(context.Context, *http.Request, string) error { return nil }

func validCID(raw string) bool {
	if _, err := syntax.ParseCID(raw); err != nil {
		return false
	}
	cid, err := cidlib.Decode(raw)
	return err == nil && cid.Version() == 1
}

// NewClient accepts an already identity-resolved HTTPS PDS origin. Discovery
// must separately enforce its SSRF policy. Redirects are never followed.
func NewClient(origin string, transport http.RoundTripper, auth Authorizer) (*Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || auth == nil {
		return nil, errors.New("verified HTTPS PDS origin and authorizer required")
	}
	if transport == nil {
		public := ssrf.PublicOnlyTransport()
		public.Proxy = nil
		public.ResponseHeaderTimeout = 10 * time.Second
		transport = public
	}
	return &Client{endpoint: u, http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, auth: auth}, nil
}

type XRPCError struct {
	Status int
	Code   string
}

func (e *XRPCError) Error() string { return fmt.Sprintf("PDS XRPC error %d (%s)", e.Status, e.Code) }

func (c *Client) request(ctx context.Context, method, nsid, repo string, query url.Values, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := *c.endpoint
	u.Path = "/xrpc/" + nsid
	u.RawQuery = query.Encode()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
		if len(data) > 1<<20 {
			return errors.New("record request exceeds byte limit")
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if err = c.auth.Authorize(ctx, r, repo); err != nil {
		return err
	}
	if r.Method != method || r.URL.Scheme != u.Scheme || r.URL.Host != u.Host || r.URL.Path != u.Path || r.URL.RawQuery != u.RawQuery || r.URL.User != nil || r.URL.Fragment != "" {
		return errors.New("authorizer changed PDS request destination")
	}
	var response *http.Response
	if c.doer != nil {
		response, err = c.doer.DoWithAuth(ctx, r, repo, syntax.NSID(nsid))
	} else {
		response, err = c.http.Do(r)
	}
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return errors.New("missing PDS response")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(payload) > 1<<20 {
		return errors.New("PDS response exceeds byte limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		if failure.Error == "RecordNotFound" && response.StatusCode == 400 {
			return ErrNotFound
		}
		if failure.Error == "InvalidSwap" {
			return ErrConflict
		}
		return &XRPCError{response.StatusCode, failure.Error}
	}
	if err = json.Unmarshal(payload, out); err != nil {
		return errors.New("invalid PDS JSON response")
	}
	return nil
}

func (c *Client) Get(ctx context.Context, m Mutation) (Record, error) {
	if err := m.Validate(); err != nil {
		return Record{}, err
	}
	var record Record
	err := c.request(ctx, "GET", "com.atproto.repo.getRecord", m.Repo, url.Values{"repo": {m.Repo}, "collection": {m.Collection}, "rkey": {m.RKey}}, nil, &record)
	if err == nil && (record.URI != m.URI() || !validCID(record.CID) || !json.Valid(record.Value)) {
		err = errors.New("invalid PDS record reference")
	}
	return record, err
}

func (c *Client) Put(ctx context.Context, m Mutation) (Reference, error) {
	if err := m.Validate(); err != nil {
		return Reference{}, err
	}
	var ref Reference
	err := c.request(ctx, "POST", "com.atproto.repo.putRecord", m.Repo, nil, m, &ref)
	if err == nil && (ref.URI != m.URI() || !validCID(ref.CID)) {
		err = errors.New("invalid PDS write reference")
	}
	return ref, err
}
