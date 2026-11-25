package tangled

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/skillpolicy"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// WriteAuthenticatedMetadataBranch reconstructs only the two source trees that
// contain the policy file using actor-authorized Bobbin reads. Untouched blob,
// symlink and Gitlink objects are referenced by hash and never downloaded. Each
// reconstructed source tree must hash to its exact base-commit object ID before
// any remote write. This works for private repositories without anonymous Git.
func (c Client) WriteAuthenticatedMetadataBranch(ctx context.Context, change MetadataChange) (MetadataBranch, error) {
	if c.Tokens == nil || c.ReadService == nil {
		return MetadataBranch{}, errors.New("authenticated Tangled metadata access is not configured")
	}
	if _, err := syntaxDID(change.ActorDID); err != nil {
		return MetadataBranch{}, err
	}
	baseRef, branchRef := plumbing.NewBranchReferenceName(change.BaseBranch), plumbing.NewBranchReferenceName(change.Branch)
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
	head, err := c.authenticatedBranch(ctx, change.ActorDID, change.RepositoryDID, change.BaseBranch)
	if err != nil {
		return MetadataBranch{}, err
	}
	if head != strings.ToLower(change.ExpectedBase) {
		return MetadataBranch{}, errors.New("Tangled base branch changed before metadata proposal")
	}
	const commitsMethod = "org.tangled.temp.git.listCommits"
	response, err := c.serviceRequest(ctx, change.ActorDID, commitsMethod, url.Values{"repos": {change.RepositoryDID}, "ranges": {head}, "limit": {"1"}, "includeStats": {"false"}})
	if err != nil {
		return MetadataBranch{}, err
	}
	var commits struct {
		Commits []struct {
			OID  string `json:"oid"`
			Tree string `json:"tree"`
		} `json:"commits"`
	}
	if err = decodeResponse(response, commitsMethod, &commits); err != nil {
		return MetadataBranch{}, err
	}
	if len(commits.Commits) != 1 || commits.Commits[0].OID != head || len(commits.Commits[0].Tree) != 40 || !oidPattern.MatchString(commits.Commits[0].Tree) {
		return MetadataBranch{}, errors.New("Tangled base commit identity did not match")
	}
	store := memory.NewStorage()
	root, err := c.authenticatedTree(ctx, change, "", plumbing.NewHash(commits.Commits[0].Tree), store)
	if err != nil {
		return MetadataBranch{}, err
	}
	var policy []object.TreeEntry
	for _, entry := range root {
		if entry.Name == ".mycontext" {
			if entry.Mode != filemode.Dir {
				return MetadataBranch{}, errors.New("policy parent is not a directory")
			}
			policy, err = c.authenticatedTree(ctx, change, ".mycontext", entry.Hash, store)
			if err != nil {
				return MetadataBranch{}, err
			}
		}
	}
	var existing []byte
	for _, entry := range policy {
		if entry.Name == "skills.yaml" {
			if !entry.Mode.IsRegular() && entry.Mode != filemode.Executable {
				return MetadataBranch{}, errors.New("policy is not a regular file")
			}
			const blobMethod = "org.tangled.temp.git.getBlob"
			response, err = c.serviceRequest(ctx, change.ActorDID, blobMethod, url.Values{"repo": {change.RepositoryDID}, "ref": {head}, "path": {".mycontext/skills.yaml"}})
			if err != nil {
				return MetadataBranch{}, err
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				return MetadataBranch{}, &StatusError{response.StatusCode, blobMethod}
			}
			existing, err = io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
			response.Body.Close()
			if err != nil || len(existing) > 256<<10 {
				return MetadataBranch{}, errors.New("policy exceeds size limit")
			}
			if plumbing.ComputeHash(plumbing.BlobObject, existing) != entry.Hash {
				return MetadataBranch{}, errors.New("Tangled policy blob integrity mismatch")
			}
		}
	}
	base := plumbing.NewHash(head)
	commit, objects, err := buildMetadataPolicyCommit(store, base, root, policy, existing, change)
	if err != nil {
		return MetadataBranch{}, err
	}
	repo, err := c.ResolveRepository(ctx, change.RepositoryDID)
	if err != nil {
		return MetadataBranch{}, err
	}
	return c.pushMetadataBranch(ctx, change, repo, store, base, commit, objects)
}

func (c Client) authenticatedTree(ctx context.Context, change MetadataChange, path string, expected plumbing.Hash, store *memory.Storage) ([]object.TreeEntry, error) {
	const treeMethod = "org.tangled.temp.git.getTree"
	response, err := c.serviceRequest(ctx, change.ActorDID, treeMethod, url.Values{"repo": {change.RepositoryDID}, "ref": {change.ExpectedBase}, "path": {path}})
	if err != nil {
		return nil, err
	}
	var tree struct {
		Entries []struct {
			Name, Mode string
			Size       int64
		} `json:"entries"`
	}
	if err = decodeResponse(response, treeMethod, &tree); err != nil {
		return nil, err
	}
	if len(tree.Entries) > 512 {
		return nil, errors.New("policy source tree exceeds entry limit")
	}
	seen := map[string]bool{}
	for _, entry := range tree.Entries {
		if entry.Name == "" || len(entry.Name) > 255 || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\x00\r\n\ufffd") || !utf8.ValidString(entry.Name) || seen[entry.Name] || entry.Size < 0 {
			return nil, errors.New("invalid Tangled source tree entry")
		}
		seen[entry.Name] = true
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	entries := make([]object.TreeEntry, len(tree.Entries))
	var group sync.WaitGroup
	slots := make(chan struct{}, 8)
	var firstErr error
	var once sync.Once
	for index, entry := range tree.Entries {
		group.Add(1)
		go func(index int, name, mode string, size int64) {
			defer group.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			fail := func(err error) { once.Do(func() { firstErr = err; cancel() }) }
			entryPath := name
			if path != "" {
				entryPath = path + "/" + name
			}
			const entryMethod = "org.tangled.temp.git.getEntry"
			response, err := c.serviceRequest(ctx, change.ActorDID, entryMethod, url.Values{"repo": {change.RepositoryDID}, "ref": {change.ExpectedBase}, "path": {entryPath}})
			if err != nil {
				fail(err)
				return
			}
			var value struct {
				Name, Mode, OID string
				Size            int64
			}
			if err = decodeResponse(response, entryMethod, &value); err != nil {
				fail(err)
				return
			}
			permissions, err := strconv.ParseUint(value.Mode, 8, 32)
			parsedMode := filemode.FileMode(permissions)
			if err != nil || parsedMode.IsMalformed() || value.Name != name || value.Mode != mode || value.Size != size || len(value.OID) != 40 || !oidPattern.MatchString(value.OID) {
				fail(errors.New("Tangled entry does not match its source tree"))
				return
			}
			entries[index] = object.TreeEntry{Name: name, Mode: parsedMode, Hash: plumbing.NewHash(value.OID)}
		}(index, entry.Name, entry.Mode, entry.Size)
	}
	group.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	actual, err := saveTree(store, entries)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, errors.New("Tangled source tree integrity mismatch")
	}
	return entries, nil
}
