package tangled

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/skillpolicy"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitHTTP "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

const maxGitPackBytes = 64 << 20
const maxGitObjectBytes = 16 << 20
const maxGitInflatedBytes = 128 << 20

type MetadataChange struct {
	ActorDID, RepositoryDID, BaseBranch, Branch, ExpectedBase string
	// CommitTime and Branch must be persisted with the operation intent, so a
	// retry creates the same commit and may recognize an already-pushed branch.
	CommitTime time.Time
	Document   map[string]any
	Exposure   string
}
type MetadataBranch struct{ Base, Head, Branch, SourceRepositoryDID string }

type gitBearer struct{ token string }

func (g gitBearer) Name() string            { return "tangled-service-jwt" }
func (g gitBearer) String() string          { return "Tangled service authorization" }
func (g gitBearer) SetAuth(r *http.Request) { r.Header.Set("Authorization", "Bearer "+g.token) }

// WriteMetadataBranch reads a bounded shallow Git pack, edits only the policy
// tree entry, and creates a new branch through compare-and-swap receive-pack.
// It needs no git executable, worktree, shell, global protocol registration, or
// temporary secret file. The default branch is never pushed or rewritten.
func (c Client) WriteMetadataBranch(ctx context.Context, change MetadataChange) (MetadataBranch, error) {
	if c.Tokens == nil {
		return MetadataBranch{}, errors.New("Tangled Git authorization is not configured")
	}
	if _, err := syntaxDID(change.ActorDID); err != nil {
		return MetadataBranch{}, err
	}
	baseRef := plumbing.NewBranchReferenceName(change.BaseBranch)
	branchRef := plumbing.NewBranchReferenceName(change.Branch)
	if change.BaseBranch == "" || change.Branch == "" || baseRef == branchRef || baseRef.Validate() != nil || branchRef.Validate() != nil || change.CommitTime.IsZero() {
		return MetadataBranch{}, errors.New("invalid metadata branch intent")
	}
	if len(change.ExpectedBase) != 40 || !oidPattern.MatchString(change.ExpectedBase) {
		return MetadataBranch{}, ErrInvalidRevision
	}
	if _, err := skillpolicy.MergeSidecar(nil, change.Document, change.Exposure); err != nil {
		return MetadataBranch{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	repo, err := c.ResolveRepository(ctx, change.RepositoryDID)
	if err != nil {
		return MetadataBranch{}, err
	}
	u, _ := url.Parse(repo.KnotURL)
	u.Path = "/" + repo.DID.String()
	ep, err := transport.NewEndpoint(u.String())
	if err != nil {
		return MetadataBranch{}, err
	}
	httpClient := c.httpClient()
	httpClient.Transport = gitTransport{inner: httpClient.Transport, origin: u.Scheme + "://" + u.Host, path: u.Path}
	protocol := gitHTTP.NewClientWithOptions(httpClient, &gitHTTP.ClientOptions{RedirectPolicy: gitHTTP.NoFollowRedirects})
	upload, err := protocol.NewUploadPackSession(ep, nil)
	if err != nil {
		return MetadataBranch{}, errors.New("Tangled Git read session failed")
	}
	defer upload.Close()
	advertised, err := upload.AdvertisedReferencesContext(ctx)
	if err != nil {
		return MetadataBranch{}, errors.New("Tangled Git reference lookup failed")
	}
	base, ok := advertised.References[baseRef.String()]
	if !ok || base.String() != strings.ToLower(change.ExpectedBase) {
		return MetadataBranch{}, errors.New("Tangled base branch changed before metadata proposal")
	}
	if !advertised.Capabilities.Supports(capability.Shallow) {
		return MetadataBranch{}, errors.New("Tangled Git server does not support bounded shallow fetch")
	}
	request := packp.NewUploadPackRequestFromCapabilities(advertised.Capabilities)
	request.Wants = []plumbing.Hash{base}
	request.Depth = packp.DepthCommits(1)
	request.Capabilities.Set(capability.Shallow)
	pack, err := upload.UploadPack(ctx, request)
	if err != nil {
		return MetadataBranch{}, errors.New("Tangled Git object fetch failed")
	}
	raw, err := io.ReadAll(io.LimitReader(pack, maxGitPackBytes+1))
	closeErr := pack.Close()
	if err != nil || closeErr != nil || len(raw) > maxGitPackBytes {
		return MetadataBranch{}, errors.New("Tangled Git pack exceeds limit or was interrupted")
	}
	store, err := readGitPack(ctx, raw)
	if err != nil {
		return MetadataBranch{}, err
	}
	head, objects, err := buildMetadataCommit(store, base, change)
	if err != nil {
		return MetadataBranch{}, err
	}
	if existing, ok := advertised.References[branchRef.String()]; ok && existing != head {
		return MetadataBranch{}, errors.New("metadata proposal branch already exists with different content")
	}
	return c.pushMetadataBranch(ctx, change, repo, store, base, head, objects)
}

func (c Client) pushMetadataBranch(ctx context.Context, change MetadataChange, repo Repository, store *memory.Storage, base, head plumbing.Hash, objects []plumbing.Hash) (MetadataBranch, error) {
	u, _ := url.Parse(repo.KnotURL)
	u.Path = "/" + repo.DID.String()
	ep, err := transport.NewEndpoint(u.String())
	if err != nil {
		return MetadataBranch{}, err
	}
	httpClient := c.httpClient()
	httpClient.Transport = gitTransport{inner: httpClient.Transport, origin: u.Scheme + "://" + u.Host, path: u.Path}
	protocol := gitHTTP.NewClientWithOptions(httpClient, &gitHTTP.ClientOptions{RedirectPolicy: gitHTTP.NoFollowRedirects})
	baseRef, branchRef := plumbing.NewBranchReferenceName(change.BaseBranch), plumbing.NewBranchReferenceName(change.Branch)
	var outgoing bytes.Buffer
	if _, err = packfile.NewEncoder(&outgoing, store, false).Encode(objects, 0); err != nil {
		return MetadataBranch{}, errors.New("could not encode metadata Git objects")
	}
	if outgoing.Len() > 1<<20 {
		return MetadataBranch{}, errors.New("metadata Git pack exceeds limit")
	}
	// Smart HTTP receive-pack checks the knot service DID rather than the repo
	// DID used by native ticket writes. Its token is reusable only until expiry.
	token, err := c.Tokens.Token(ctx, change.ActorDID, "did:web:"+u.Hostname(), "org.tangled.git.receivePack")
	if err != nil {
		return MetadataBranch{}, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n ") {
		return MetadataBranch{}, errors.New("invalid Tangled Git authorization")
	}
	push, err := protocol.NewReceivePackSession(ep, gitBearer{token})
	if err != nil {
		return MetadataBranch{}, errors.New("Tangled Git write session failed")
	}
	defer push.Close()
	refs, err := push.AdvertisedReferencesContext(ctx)
	if err != nil {
		return MetadataBranch{}, errors.New("Tangled Git write access denied")
	}
	result := MetadataBranch{base.String(), head.String(), change.Branch, change.RepositoryDID}
	if existing, ok := refs.References[branchRef.String()]; ok {
		if existing == head {
			return result, nil
		}
		return MetadataBranch{}, errors.New("metadata proposal branch changed concurrently")
	}
	if refs.References[baseRef.String()] != base {
		return MetadataBranch{}, errors.New("Tangled base branch changed before push")
	}
	if !refs.Capabilities.Supports(capability.ReportStatus) {
		return MetadataBranch{}, errors.New("Tangled Git server cannot confirm push status")
	}
	update := packp.NewReferenceUpdateRequestFromCapabilities(refs.Capabilities)
	update.Commands = []*packp.Command{{Name: branchRef, Old: plumbing.ZeroHash, New: head}}
	update.Packfile = io.NopCloser(bytes.NewReader(outgoing.Bytes()))
	status, err := push.ReceivePack(ctx, update)
	if err != nil || status == nil || status.Error() != nil {
		return MetadataBranch{}, errors.New("Tangled Git branch push was not confirmed")
	}
	if len(status.CommandStatuses) != 1 || status.CommandStatuses[0].ReferenceName != branchRef {
		return MetadataBranch{}, errors.New("Tangled Git push reference was not confirmed")
	}
	return result, nil
}

func syntaxDID(raw string) (string, error) {
	did, err := syntax.ParseDID(raw)
	if err != nil {
		return "", ErrInvalidRepository
	}
	return did.String(), nil
}

type gitTransport struct {
	inner        http.RoundTripper
	origin, path string
}

func (t gitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != t.origin || r.URL.Path != t.path+"/info/refs" && r.URL.Path != t.path+"/git-upload-pack" && r.URL.Path != t.path+"/git-receive-pack" {
		return nil, errors.New("Tangled Git transport destination changed")
	}
	response, err := t.inner.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > maxGitPackBytes {
		response.Body.Close()
		return nil, errors.New("Tangled Git response exceeds limit")
	}
	response.Body = &limitedGitBody{ReadCloser: response.Body, remaining: maxGitPackBytes}
	return response, nil
}

type limitedGitBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedGitBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, errors.New("Tangled Git response exceeds limit")
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func readGitPack(ctx context.Context, raw []byte) (*memory.Storage, error) {
	scanner := packfile.NewScanner(contextReader{ctx, bytes.NewReader(raw)})
	_, count, err := scanner.Header()
	if err != nil || count > 50000 {
		return nil, errors.New("invalid or oversized Tangled Git object count")
	}
	total := int64(0)
	for range count {
		header, err := scanner.NextObjectHeader()
		if err != nil || header.Length < 0 || header.Length > maxGitObjectBytes {
			return nil, errors.New("Tangled Git object exceeds limit")
		}
		var prefix bytes.Buffer
		writer := io.Writer(io.Discard)
		if header.Type == plumbing.REFDeltaObject || header.Type == plumbing.OFSDeltaObject {
			writer = &prefixWriter{prefix: &prefix}
		}
		if _, _, err = scanner.NextObject(writer); err != nil {
			return nil, errors.New("invalid Tangled Git object")
		}
		size := header.Length
		if header.Type == plumbing.REFDeltaObject || header.Type == plumbing.OFSDeltaObject {
			base, n := binary.Uvarint(prefix.Bytes())
			if n <= 0 || base > maxGitObjectBytes {
				return nil, errors.New("invalid Tangled delta base")
			}
			target, n := binary.Uvarint(prefix.Bytes()[n:])
			if n <= 0 || target > maxGitObjectBytes {
				return nil, errors.New("Tangled delta target exceeds limit")
			}
			size = int64(target)
		}
		total += size
		if total > maxGitInflatedBytes {
			return nil, errors.New("Tangled Git expansion exceeds limit")
		}
	}
	store := memory.NewStorage()
	parser, err := packfile.NewParserWithStorage(packfile.NewScanner(contextReader{ctx, bytes.NewReader(raw)}), store)
	if err != nil {
		return nil, err
	}
	if _, err = parser.Parse(); err != nil {
		return nil, errors.New("invalid Tangled Git pack integrity")
	}
	return store, nil
}

type prefixWriter struct{ prefix *bytes.Buffer }

func (w *prefixWriter) Write(p []byte) (int, error) {
	n := len(p)
	keep := 20 - w.prefix.Len()
	if keep > len(p) {
		keep = len(p)
	}
	if keep > 0 {
		w.prefix.Write(p[:keep])
	}
	return n, nil
}

func buildMetadataCommit(store *memory.Storage, base plumbing.Hash, change MetadataChange) (plumbing.Hash, []plumbing.Hash, error) {
	commit, err := object.GetCommit(store, base)
	if err != nil {
		return plumbing.ZeroHash, nil, errors.New("Tangled base commit missing")
	}
	root, err := commit.Tree()
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	rootEntries := append([]object.TreeEntry(nil), root.Entries...)
	policyTree := &object.Tree{}
	for _, entry := range rootEntries {
		if entry.Name == ".mycontext" {
			if entry.Mode != filemode.Dir {
				return plumbing.ZeroHash, nil, errors.New("policy parent is not a directory")
			}
			policyTree, err = object.GetTree(store, entry.Hash)
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
		}
	}
	entries := append([]object.TreeEntry(nil), policyTree.Entries...)
	var existing []byte
	for _, entry := range entries {
		if entry.Name == "skills.yaml" {
			if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable {
				return plumbing.ZeroHash, nil, errors.New("policy is not a regular file")
			}
			blob, err := object.GetBlob(store, entry.Hash)
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
			if blob.Size > 256<<10 {
				return plumbing.ZeroHash, nil, errors.New("policy exceeds size limit")
			}
			r, err := blob.Reader()
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
			existing, err = io.ReadAll(io.LimitReader(r, (256<<10)+1))
			r.Close()
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
		}
	}
	return buildMetadataPolicyCommit(store, base, rootEntries, entries, existing, change)
}

func buildMetadataPolicyCommit(store *memory.Storage, base plumbing.Hash, rootEntries, entries []object.TreeEntry, existing []byte, change MetadataChange) (plumbing.Hash, []plumbing.Hash, error) {
	content, err := skillpolicy.MergeSidecar(existing, change.Document, change.Exposure)
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	if bytes.Equal(content, existing) {
		return plumbing.ZeroHash, nil, errors.New("metadata policy already matches")
	}
	blob := store.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(content)))
	w, err := blob.Writer()
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	if _, err = w.Write(content); err != nil {
		return plumbing.ZeroHash, nil, err
	}
	if err = w.Close(); err != nil {
		return plumbing.ZeroHash, nil, err
	}
	blobHash, err := store.SetEncodedObject(blob)
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	entries = replaceTreeEntry(entries, object.TreeEntry{Name: "skills.yaml", Mode: filemode.Regular, Hash: blobHash})
	policyHash, err := saveTree(store, entries)
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	rootEntries = replaceTreeEntry(rootEntries, object.TreeEntry{Name: ".mycontext", Mode: filemode.Dir, Hash: policyHash})
	rootHash, err := saveTree(store, rootEntries)
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	signature := object.Signature{Name: "MyContextProtocol", Email: "metadata@mycontextprotocol.dev", When: change.CommitTime.UTC()}
	updated := object.Commit{Author: signature, Committer: signature, Message: "Update checksum-bound skill runtime policy\n\nRequested by " + change.ActorDID + "\n", TreeHash: rootHash, ParentHashes: []plumbing.Hash{base}}
	encoded := store.NewEncodedObject()
	if err = updated.Encode(encoded); err != nil {
		return plumbing.ZeroHash, nil, err
	}
	head, err := store.SetEncodedObject(encoded)
	return head, []plumbing.Hash{blobHash, policyHash, rootHash, head}, err
}
func replaceTreeEntry(entries []object.TreeEntry, value object.TreeEntry) []object.TreeEntry {
	for i, entry := range entries {
		if entry.Name == value.Name {
			entries[i] = value
			return entries
		}
	}
	return append(entries, value)
}
func saveTree(store *memory.Storage, entries []object.TreeEntry) (plumbing.Hash, error) {
	sort.Sort(object.TreeEntrySorter(entries))
	encoded := store.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(encoded); err != nil {
		return plumbing.ZeroHash, err
	}
	return store.SetEncodedObject(encoded)
}
