// Package tangled implements bounded public repository reads against pinned
// Tangled knot contracts. It does not claim private-access or proposal parity.
package tangled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util/ssrf"
)

const UpstreamCommit = "d09a20049c96cc6618ab7825485e04192e5a5840"
const branchMethod = "org.tangled.git.getBranch"
const archiveMethod = "org.tangled.git.getArchive"

var ErrInvalidRepository = errors.New("invalid Tangled repository identity")
var ErrInvalidRevision = errors.New("invalid Tangled immutable revision")
var oidPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

// Repository identifies Git storage by its native repository DID. Mutable account
// handles and repository names are never used as authoritative knot locations.
type Repository struct {
	DID     syntax.DID
	KnotURL string
}

type StatusError struct {
	Status int
	Method string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("Tangled %s failed: HTTP %d", e.Method, e.Status)
}

// Client.HTTP and Directory may be injected by trusted callers for testing. The
// zero value uses SSRF-safe, proxy-free HTTP for both DID resolution and reads.
type Client struct {
	HTTP      *http.Client
	Directory identity.Directory
	Limits    reposync.Limits
	TempDir   string
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		copy := *c.HTTP
		copy.CheckRedirect = noRedirect
		if copy.Timeout <= 0 {
			copy.Timeout = 60 * time.Second
		}
		return &copy
	}
	transport := ssrf.PublicOnlyTransport()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: checkedTransport{transport}, Timeout: 60 * time.Second, CheckRedirect: noRedirect}
}
func noRedirect(*http.Request, []*http.Request) error {
	return errors.New("Tangled redirects are disabled")
}

type checkedTransport struct{ inner http.RoundTripper }

func (t checkedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if _, err := validateURL(r.URL.String(), false); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(r)
}
func validateURL(raw string, origin bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrInvalidRepository
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Port() != "" || (origin && (u.RawQuery != "" || (u.Path != "" && u.Path != "/"))) {
		return nil, ErrInvalidRepository
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ssrf.IsPublicIPAddress(ip) {
			return nil, ErrInvalidRepository
		}
	} else {
		if _, err := syntax.ParseHandle(host); err != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".") {
			return nil, ErrInvalidRepository
		}
	}
	return u, nil
}
func (c Client) ResolveRepository(ctx context.Context, rawDID string) (Repository, error) {
	did, err := syntax.ParseDID(rawDID)
	if err != nil {
		return Repository{}, ErrInvalidRepository
	}
	directory := c.Directory
	if directory == nil {
		client := c.httpClient()
		directory = &identity.BaseDirectory{PLCURL: "https://plc.directory", HTTPClient: *client, PLCClient: client, UserAgent: "MyContextProtocol/tangled"}
	}
	ident, err := directory.LookupDID(ctx, did)
	if err != nil {
		return Repository{}, err
	}
	if ident == nil || ident.DID != did {
		return Repository{}, ErrInvalidRepository
	}
	service := ident.Services["tangled_knot"]
	if service.Type != "TangledKnot" {
		return Repository{}, ErrInvalidRepository
	}
	endpoint, err := validateURL(service.URL, true)
	if err != nil {
		return Repository{}, err
	}
	return Repository{DID: did, KnotURL: strings.TrimSuffix(endpoint.String(), "/")}, nil
}
func (c Client) request(ctx context.Context, repo Repository, method string, params url.Values) (*http.Response, error) {
	endpoint, err := validateURL(repo.KnotURL, true)
	if err != nil {
		return nil, err
	}
	endpoint.Path = "/xrpc/" + method
	params.Set("repo", repo.DID.String())
	endpoint.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MyContextProtocol/tangled")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &StatusError{Status: resp.StatusCode, Method: method}
	}
	return resp, nil
}
func (c Client) resolveBranch(ctx context.Context, repo Repository, branch string) (string, error) {
	if branch == "" || len(branch) > 255 || strings.ContainsAny(branch, "\x00\r\n") {
		return "", ErrInvalidRevision
	}
	resp, err := c.request(ctx, repo, branchMethod, url.Values{"name": {branch}})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", ErrInvalidRevision
	}
	var result struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	}
	if json.Unmarshal(data, &result) != nil || result.Name != branch || !oidPattern.MatchString(result.Hash) {
		return "", ErrInvalidRevision
	}
	return strings.ToLower(result.Hash), nil
}
func (c Client) ResolveBranch(ctx context.Context, repoDID, branch string) (string, error) {
	repo, err := c.ResolveRepository(ctx, repoDID)
	if err != nil {
		return "", err
	}
	return c.resolveBranch(ctx, repo, branch)
}

// Snapshot resolves the branch once, then requests its immutable object ID to
// avoid a moving-branch race. It never attaches GitHub or account credentials.
func (c Client) Snapshot(ctx context.Context, repoDID, branch string) (reposync.Outcome, error) {
	timeout := c.Limits.FetchTimeout
	if timeout <= 0 {
		timeout = reposync.DefaultLimits().FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	repo, err := c.ResolveRepository(ctx, repoDID)
	if err != nil {
		return reposync.Outcome{}, err
	}
	revision, err := c.resolveBranch(ctx, repo, branch)
	if err != nil {
		return reposync.Outcome{}, err
	}
	resp, err := c.request(ctx, repo, archiveMethod, url.Values{"ref": {revision}, "format": {"tar.gz"}, "prefix": {"mycontext"}})
	if err != nil {
		return reposync.Outcome{}, err
	}
	defer resp.Body.Close()
	max := c.Limits.ArchiveBytes
	if max <= 0 {
		max = reposync.DefaultLimits().ArchiveBytes
	}
	if resp.ContentLength > max {
		return reposync.Outcome{}, reposync.ErrArchiveTooLarge
	}
	// The extractor independently bounds compressed bytes, expansion, files,
	// traversal, links and special entries, and removes partial trees on failure.
	out, err := (reposync.Fetcher{Limits: c.Limits, TempDir: c.TempDir}).Extract(ctx, resp.Body)
	if err != nil {
		return reposync.Outcome{}, err
	}
	if out.ResolvedCommitSHA != "" && out.ResolvedCommitSHA != revision {
		out.Cleanup()
		return reposync.Outcome{}, ErrInvalidRevision
	}
	out.ResolvedCommitSHA = revision
	return out, nil
}
