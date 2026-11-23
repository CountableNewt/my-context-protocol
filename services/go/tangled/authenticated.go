package tangled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// ServiceTokens mints a fresh, method-bound service JWT for the authenticated
// actor. Tokens must be short-lived and issued by that actor's verified PDS.
// PDS OAuth access tokens must never be returned by this interface.
type ServiceTokens interface {
	Token(context.Context, string, string, string) (string, error)
}

// ReadService is operator-reviewed configuration, never caller-controlled input.
// Its DID is the audience of Bobbin's authenticated XRPC methods. Bobbin redirects
// archives to its configured mirror; only this exact verified origin is accepted.
type ReadService struct{ Origin, DID, ArchiveMirrorOrigin string }

func (c Client) serviceRequest(ctx context.Context, actor, method string, params url.Values) (*http.Response, error) {
	if c.ReadService == nil || c.Tokens == nil {
		return nil, errors.New("authenticated Tangled read service is not configured")
	}
	if _, err := syntax.ParseDID(actor); err != nil {
		return nil, ErrInvalidRepository
	}
	audience := strings.Split(c.ReadService.DID, "#")
	if len(audience) > 2 || len(audience) == 2 && (audience[1] == "" || strings.ContainsAny(audience[1], "/? \t\r\n")) {
		return nil, ErrInvalidRepository
	}
	if _, err := syntax.ParseDID(audience[0]); err != nil {
		return nil, ErrInvalidRepository
	}
	u, err := validateURL(c.ReadService.Origin, true)
	if err != nil {
		return nil, err
	}
	u.Path = "/xrpc/" + method
	u.RawQuery = params.Encode()
	token, err := c.Tokens.Token(ctx, actor, c.ReadService.DID, method)
	if err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n ") {
		return nil, errors.New("invalid Tangled service authorization")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("User-Agent", "MyContextProtocol/tangled")
	client := c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(r)
}

func decodeResponse(resp *http.Response, method string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Status: resp.StatusCode, Method: method}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
		return errors.New("invalid Tangled JSON response")
	}
	return nil
}

type RepositoryView struct {
	RepositoryDID string `json:"repository_did"`
	OwnerDID      string `json:"owner_did"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	URI           string `json:"uri"`
	CID           string `json:"cid"`
}

type RepositoryPage struct {
	Repositories []RepositoryView `json:"repositories"`
	Cursor       string           `json:"cursor,omitempty"`
}

// ListRepositories lists the owner's public manifests through Bobbin. Private
// manifests are accessible by exact authenticated LookupRepository: upstream's
// listRepos handler does not merge its private-space index into public lists.
func (c Client) ListRepositories(ctx context.Context, actor, owner, cursor string, limit int) (RepositoryPage, error) {
	if _, err := syntax.ParseDID(owner); err != nil {
		return RepositoryPage{}, ErrInvalidRepository
	}
	if limit < 1 || limit > 100 || len(cursor) > 4096 {
		return RepositoryPage{}, errors.New("invalid Tangled repository page")
	}
	params := url.Values{"subject": {owner}, "limit": {fmt.Sprint(limit)}, "order": {"desc"}}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	const method = "org.tangled.repo.listRepos"
	resp, err := c.serviceRequest(ctx, actor, method, params)
	if err != nil {
		return RepositoryPage{}, err
	}
	var page struct {
		Items  []manifestResponse `json:"items"`
		Cursor string             `json:"cursor"`
	}
	if err = decodeResponse(resp, method, &page); err != nil {
		return RepositoryPage{}, err
	}
	if len(page.Items) > limit || len(page.Cursor) > 4096 {
		return RepositoryPage{}, errors.New("invalid Tangled repository page response")
	}
	out := RepositoryPage{Repositories: make([]RepositoryView, 0, len(page.Items)), Cursor: page.Cursor}
	for _, item := range page.Items {
		view, err := manifestView(item, owner, "")
		if err != nil {
			return RepositoryPage{}, err
		}
		out.Repositories = append(out.Repositories, view)
	}
	return out, nil
}

type manifestResponse struct {
	URI   string `json:"uri"`
	CID   string `json:"cid"`
	Value struct {
		Type        string `json:"$type"`
		Declaration struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
		} `json:"declaration"`
	} `json:"value"`
	Git struct {
		DefaultBranch string `json:"defaultBranch"`
	} `json:"git"`
}

func manifestView(value manifestResponse, owner, name string) (RepositoryView, error) {
	uri, err := syntax.ParseATURI(value.URI)
	if err != nil || uri.Collection().String() != "org.tangled.repo.manifest" || uri.RecordKey().String() != "self" || value.Value.Type != "org.tangled.repo.manifest" || value.Value.Declaration.Owner != owner || name != "" && !strings.EqualFold(value.Value.Declaration.Name, name) {
		return RepositoryView{}, ErrInvalidRepository
	}
	did, err := syntax.ParseDID(uri.Authority().String())
	if err != nil {
		return RepositoryView{}, ErrInvalidRepository
	}
	if _, err = syntax.ParseRecordKey(value.Value.Declaration.Name); err != nil {
		return RepositoryView{}, ErrInvalidRepository
	}
	if _, err = syntax.ParseCID(value.CID); err != nil {
		return RepositoryView{}, ErrInvalidRepository
	}
	return RepositoryView{did.String(), owner, value.Value.Declaration.Name, value.Git.DefaultBranch, value.URI, value.CID}, nil
}

// LookupRepository performs authenticated discovery. The manifest identifies
// both the native repository DID and its declaration owner; mutable handles are
// never accepted as authorization or authoritative repository identity.
func (c Client) LookupRepository(ctx context.Context, actor, owner, name string) (RepositoryView, error) {
	if _, err := syntax.ParseDID(owner); err != nil {
		return RepositoryView{}, ErrInvalidRepository
	}
	if _, err := syntax.ParseRecordKey(name); err != nil {
		return RepositoryView{}, ErrInvalidRepository
	}
	const method = "org.tangled.temp.repo.getRepoByName"
	resp, err := c.serviceRequest(ctx, actor, method, url.Values{"owner": {owner}, "name": {name}})
	if err != nil {
		return RepositoryView{}, err
	}
	var value manifestResponse
	if err = decodeResponse(resp, method, &value); err != nil {
		return RepositoryView{}, err
	}
	if value.Git.DefaultBranch == "" {
		return RepositoryView{}, ErrInvalidRepository
	}
	return manifestView(value, owner, name)
}

// IsPublicRepository classifies visibility only after an authenticated lookup.
// It proves that the same manifest is independently available without an actor;
// denial or network ambiguity must be treated as private by callers.
func (c Client) IsPublicRepository(ctx context.Context, owner, name, repoDID string) (bool, error) {
	if c.ReadService == nil {
		return false, errors.New("Tangled read service is not configured")
	}
	u, err := validateURL(c.ReadService.Origin, true)
	if err != nil {
		return false, err
	}
	u.Path = "/xrpc/org.tangled.temp.repo.getRepoByName"
	u.RawQuery = url.Values{"owner": {owner}, "name": {name}}.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	response, err := c.httpClient().Do(r)
	if err != nil {
		return false, err
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return false, nil
	}
	var value manifestResponse
	if err = decodeResponse(response, "org.tangled.temp.repo.getRepoByName", &value); err != nil {
		return false, err
	}
	view, err := manifestView(value, owner, name)
	if err != nil {
		return false, err
	}
	return view.RepositoryDID == repoDID, nil
}

func (c Client) authenticatedBranch(ctx context.Context, actor, repo, branch string) (string, error) {
	if _, err := syntax.ParseDID(repo); err != nil {
		return "", ErrInvalidRepository
	}
	if branch == "" || len(branch) > 244 || strings.ContainsAny(branch, "\x00\r\n") {
		return "", ErrInvalidRevision
	}
	const method = "org.tangled.temp.git.getBranch"
	resp, err := c.serviceRequest(ctx, actor, method, url.Values{"repo": {repo}, "name": {branch}})
	if err != nil {
		return "", err
	}
	var value struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	}
	if err = decodeResponse(resp, method, &value); err != nil {
		return "", err
	}
	if value.Name != branch || !oidPattern.MatchString(value.Hash) {
		return "", ErrInvalidRevision
	}
	return strings.ToLower(value.Hash), nil
}

func (c Client) ResolveAuthenticatedBranch(ctx context.Context, actor, repo, branch string) (string, error) {
	return c.authenticatedBranch(ctx, actor, repo, branch)
}

func (c Client) AuthenticatedSnapshot(ctx context.Context, actor, repo, branch string) (reposync.Outcome, error) {
	timeout := c.Limits.FetchTimeout
	if timeout <= 0 {
		timeout = reposync.DefaultLimits().FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	revision, err := c.authenticatedBranch(ctx, actor, repo, branch)
	if err != nil {
		return reposync.Outcome{}, err
	}
	return c.AuthenticatedRevision(ctx, actor, repo, revision)
}

// AuthenticatedRevision hydrates derived source content at the exact release
// revision, without resolving a moving branch during projection recovery.
func (c Client) AuthenticatedRevision(ctx context.Context, actor, repo, revision string) (reposync.Outcome, error) {
	if !oidPattern.MatchString(revision) {
		return reposync.Outcome{}, ErrInvalidRevision
	}
	const method = "org.tangled.temp.git.getArchive"
	params := url.Values{"repo": {repo}, "ref": {revision}, "format": {"tar.gz"}, "prefix": {"mycontext"}}
	resp, err := c.serviceRequest(ctx, actor, method, params)
	if err != nil {
		return reposync.Outcome{}, err
	}
	if resp.StatusCode == http.StatusFound {
		location := resp.Header.Get("Location")
		resp.Body.Close()
		mirror, mirrorErr := validateURL(c.ReadService.ArchiveMirrorOrigin, true)
		target, targetErr := validateURL(location, false)
		if mirrorErr != nil || targetErr != nil || target.Scheme != mirror.Scheme || target.Host != mirror.Host || target.Path != "/xrpc/"+method || target.Query().Encode() != params.Encode() {
			return reposync.Outcome{}, errors.New("unverified Tangled archive redirect")
		}
		// Archive redirects are a separate anonymous request. No Bobbin JWT,
		// cookies or headers may cross the service audience boundary.
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		resp, err = c.httpClient().Do(r)
		if err != nil {
			return reposync.Outcome{}, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return reposync.Outcome{}, &StatusError{resp.StatusCode, method}
	}
	max := c.Limits.ArchiveBytes
	if max <= 0 {
		max = reposync.DefaultLimits().ArchiveBytes
	}
	if resp.ContentLength > max {
		return reposync.Outcome{}, reposync.ErrArchiveTooLarge
	}
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
