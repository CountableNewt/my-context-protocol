package tangled

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/storage/memory"
)

func gitFixture(t *testing.T) (*memory.Storage, plumbing.Hash, []plumbing.Hash) {
	t.Helper()
	store := memory.NewStorage()
	blob := store.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(8)
	w, _ := blob.Writer()
	w.Write([]byte("original"))
	w.Close()
	blobHash, err := store.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := saveTree(store, []object.TreeEntry{{Name: "SKILL.md", Mode: filemode.Regular, Hash: blobHash}})
	if err != nil {
		t.Fatal(err)
	}
	sign := object.Signature{Name: "Original author", Email: "author@example.com", When: time.Unix(1000, 0)}
	commit := object.Commit{Author: sign, Committer: sign, TreeHash: tree, Message: "Initial\n"}
	encoded := store.NewEncodedObject()
	commit.Encode(encoded)
	base, err := store.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return store, base, []plumbing.Hash{blobHash, tree, base}
}
func metadataChange(base plumbing.Hash) MetadataChange {
	return MetadataChange{ActorDID: actorDID, RepositoryDID: repoDID, BaseBranch: "main", Branch: "mycontext/metadata/operation", ExpectedBase: base.String(), CommitTime: time.Unix(2000, 0), Exposure: "tool", Document: map[string]any{"id": "guide", "source": map[string]any{"checksum": strings.Repeat("a", 64)}, "validation": map[string]any{"clarificationRequired": false}, "scope": "repository"}}
}
func advertisedGit(t *testing.T, service string, refs map[string]plumbing.Hash) *http.Response {
	t.Helper()
	adv := packp.NewAdvRefs()
	adv.References = refs
	adv.Capabilities.Set(capability.Shallow)
	adv.Capabilities.Set(capability.ReportStatus)
	var data bytes.Buffer
	encoder := pktline.NewEncoder(&data)
	encoder.EncodeString("# service=" + service + "\n")
	encoder.Flush()
	if err := adv.Encode(&data); err != nil {
		t.Fatal(err)
	}
	resp := response(200, data.Bytes())
	resp.Header.Set("Content-Type", "application/x-"+service+"-advertisement")
	return resp
}

func TestMetadataGitWriteUsesNativeBoundedTransportAndOnlyCreatesProposalBranch(t *testing.T) {
	store, base, objects := gitFixture(t)
	change := metadataChange(base)
	var pack bytes.Buffer
	if _, err := packfile.NewEncoder(&pack, store, false).Encode(objects, 0); err != nil {
		t.Fatal(err)
	}
	var pushed plumbing.Hash
	pushes := 0
	calls := 0
	c := clientFor(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "knot.example" || !strings.HasPrefix(r.URL.Path, "/"+repoDID+"/") {
			t.Fatal("unverified Git URL")
		}
		refs := map[string]plumbing.Hash{"refs/heads/main": base}
		if pushed != plumbing.ZeroHash {
			refs["refs/heads/"+change.Branch] = pushed
		}
		switch {
		case r.URL.Path == "/"+repoDID+"/info/refs":
			service := r.URL.Query().Get("service")
			if service == "git-receive-pack" && r.Header.Get("Authorization") != "Bearer push-jwt" {
				t.Fatal("push discovery lacked authorization")
			}
			return advertisedGit(t, service, refs), nil
		case strings.HasSuffix(r.URL.Path, "git-upload-pack"):
			if r.Header.Get("Authorization") != "" {
				t.Fatal("write token used for read")
			}
			var body bytes.Buffer
			encoder := pktline.NewEncoder(&body)
			encoder.Encodef("shallow %s\n", base.String())
			encoder.Flush()
			encoder.EncodeString("NAK\n")
			body.Write(pack.Bytes())
			resp := response(200, body.Bytes())
			resp.Header.Set("Content-Type", "application/x-git-upload-pack-result")
			return resp, nil
		case strings.HasSuffix(r.URL.Path, "git-receive-pack"):
			if r.Header.Get("Authorization") != "Bearer push-jwt" {
				t.Fatal("push lacked authorization")
			}
			update := packp.NewReferenceUpdateRequest()
			if err := update.Decode(r.Body); err != nil {
				t.Fatal(err)
			}
			if len(update.Commands) != 1 || update.Commands[0].Name.String() != "refs/heads/"+change.Branch || update.Commands[0].Old != plumbing.ZeroHash {
				t.Fatal("push overwrites existing branch")
			}
			pushed = update.Commands[0].New
			pushes++
			if err := packfile.UpdateObjectStorage(store, update.Packfile); err != nil {
				t.Fatal(err)
			}
			status := packp.ReportStatus{UnpackStatus: "ok", CommandStatuses: []*packp.CommandStatus{{ReferenceName: update.Commands[0].Name, Status: "ok"}}}
			var body bytes.Buffer
			status.Encode(&body)
			resp := response(200, body.Bytes())
			resp.Header.Set("Content-Type", "application/x-git-receive-pack-result")
			return resp, nil
		default:
			t.Fatal(r.URL)
			return nil, nil
		}
	})
	c.Tokens = tokenFunc(func(_ context.Context, actor, audience, method string) (string, error) {
		if actor != actorDID || audience != "did:web:knot.example" || method != "org.tangled.git.receivePack" {
			t.Fatal("wrong Git service grant")
		}
		return "push-jwt", nil
	})
	result, err := c.WriteMetadataBranch(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	if result.Base != base.String() || result.Head != pushed.String() || pushes != 1 || calls != 4 {
		t.Fatalf("unexpected Git sequence %+v pushes=%d calls=%d", result, pushes, calls)
	}
	commit, err := object.GetCommit(store, pushed)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.ParentHashes) != 1 || commit.ParentHashes[0] != base {
		t.Fatal("proposal detached from source history")
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	file, err := tree.File("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := file.Contents()
	if original != "original" {
		t.Fatal("source content changed")
	}
	policy, err := tree.File(".mycontext/skills.yaml")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := policy.Contents()
	if !strings.Contains(content, "base_checksum: "+strings.Repeat("a", 64)) {
		t.Fatal("policy provenance missing")
	}
	// Persisted operation branch/time yield the same object ID. A confirmed
	// existing branch returns success without another externally visible push.
	retried, err := c.WriteMetadataBranch(context.Background(), change)
	if err != nil || retried.Head != result.Head || pushes != 1 {
		t.Fatalf("retry changed Git intent %+v %v", retried, err)
	}
}

func TestMetadataCommitIsDeterministicAndRejectsPolicySymlink(t *testing.T) {
	store, base, _ := gitFixture(t)
	change := metadataChange(base)
	a, _, err := buildMetadataCommit(store, base, change)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := buildMetadataCommit(store, base, change)
	if err != nil || a != b {
		t.Fatal("durable intent is not deterministic")
	}
	parent, err := object.GetCommit(store, base)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := parent.Tree()
	root.Entries = append(root.Entries, object.TreeEntry{Name: ".mycontext", Mode: filemode.Symlink, Hash: root.Entries[0].Hash})
	parent.TreeHash, _ = saveTree(store, root.Entries)
	encoded := store.NewEncodedObject()
	parent.Encode(encoded)
	malicious, _ := store.SetEncodedObject(encoded)
	if _, _, err := buildMetadataCommit(store, malicious, change); err == nil {
		t.Fatal("symlink policy parent accepted")
	}
}
func TestGitPackPreflightRejectsUnboundedCountAndCorruption(t *testing.T) {
	var header bytes.Buffer
	header.WriteString("PACK")
	binary.Write(&header, binary.BigEndian, uint32(2))
	binary.Write(&header, binary.BigEndian, uint32(50001))
	if _, err := readGitPack(context.Background(), header.Bytes()); err == nil {
		t.Fatal("unbounded object count accepted")
	}
	if _, err := readGitPack(context.Background(), []byte("not a pack")); err == nil {
		t.Fatal("invalid pack accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readGitPack(ctx, header.Bytes()); err == nil {
		t.Fatal("cancelled Git operation continued")
	}
}
func TestGitTransportRejectsUnexpectedDestination(t *testing.T) {
	inner := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected Git route reached network")
		return nil, nil
	})
	for _, target := range []string{"https://evil.example/" + repoDID + "/git-receive-pack", "https://knot.example/other/git-receive-pack", "http://knot.example/" + repoDID + "/git-receive-pack"} {
		r, _ := http.NewRequest("POST", target, io.NopCloser(strings.NewReader("pack")))
		if _, err := (gitTransport{inner: inner, origin: "https://knot.example", path: "/" + repoDID}).RoundTrip(r); err == nil {
			t.Fatal("unverified destination accepted")
		}
	}
}
