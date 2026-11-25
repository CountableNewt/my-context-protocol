package tangled

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

func TestAuthenticatedTreeMetadataWritePreservesUntouchedObjectsWithoutAnonymousReads(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			store, base, _ := gitFixture(t)
			original, _ := object.GetCommit(store, base)
			tree, _ := original.Tree()
			entries := append([]object.TreeEntry(nil), tree.Entries...)
			entries = append(entries, object.TreeEntry{Name: "dependency", Mode: filemode.Submodule, Hash: plumbing.NewHash(strings.Repeat("c", 40))}, object.TreeEntry{Name: "link", Mode: filemode.Symlink, Hash: plumbing.NewHash(strings.Repeat("d", 40))})
			existing := []byte("version: 1\nskills: {}\ncustom: retained\n")
			blob := store.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			blob.SetSize(int64(len(existing)))
			writer, _ := blob.Writer()
			writer.Write(existing)
			writer.Close()
			blobHash, _ := store.SetEncodedObject(blob)
			policyEntries := []object.TreeEntry{{Name: "skills.yaml", Mode: filemode.Regular, Hash: blobHash}}
			policyHash, _ := saveTree(store, policyEntries)
			entries = append(entries, object.TreeEntry{Name: ".mycontext", Mode: filemode.Dir, Hash: policyHash})
			rootHash, _ := saveTree(store, entries)
			original.TreeHash = rootHash
			encoded := store.NewEncodedObject()
			original.Encode(encoded)
			base, _ = store.SetEncodedObject(encoded)
			change := metadataChange(base)
			pushes := 0
			c := clientFor(t, func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "git-upload-pack") {
					t.Fatal("private source read through anonymous Git")
				}
				if r.URL.Host == "bobbin.example" {
					if r.Header.Get("Authorization") != "Bearer bobbin-jwt" {
						t.Fatal("private source request lacked actor authorization")
					}
					var value any
					switch r.URL.Path {
					case "/xrpc/org.tangled.temp.git.getBranch":
						value = map[string]any{"name": "main", "hash": base.String()}
					case "/xrpc/org.tangled.temp.git.listCommits":
						if r.URL.Query().Get("ranges") != base.String() {
							t.Fatal("commit not pinned")
						}
						value = map[string]any{"commits": []any{map[string]any{"oid": base.String(), "tree": rootHash.String()}}}
					case "/xrpc/org.tangled.temp.git.getTree":
						if r.URL.Query().Get("ref") != base.String() {
							t.Fatal("tree not pinned")
						}
						out := []map[string]any{}
						treeEntries := entries
						if r.URL.Query().Get("path") == ".mycontext" {
							treeEntries = policyEntries
						}
						for _, entry := range treeEntries {
							size := int64(0)
							if entry.Name == "SKILL.md" {
								size = 8
							}
							if entry.Name == "link" {
								size = 12
							}
							if entry.Name == "skills.yaml" {
								size = int64(len(existing))
							}
							out = append(out, map[string]any{"name": entry.Name, "mode": fmt.Sprintf("%07o", entry.Mode), "size": size})
						}
						value = map[string]any{"entries": out}
					case "/xrpc/org.tangled.temp.git.getEntry":
						if r.URL.Query().Get("ref") != base.String() {
							t.Fatal("entry not pinned")
						}
						name := r.URL.Query().Get("path")
						treeEntries := entries
						if strings.HasPrefix(name, ".mycontext/") {
							treeEntries = policyEntries
							name = strings.TrimPrefix(name, ".mycontext/")
						}
						for _, entry := range treeEntries {
							if entry.Name == name {
								size := int64(0)
								if name == "SKILL.md" {
									size = 8
								}
								if name == "link" {
									size = 12
								}
								if name == "skills.yaml" {
									size = int64(len(existing))
								}
								hash := entry.Hash.String()
								if corrupt && name == "SKILL.md" {
									hash = strings.Repeat("f", 40)
								}
								value = map[string]any{"name": name, "mode": fmt.Sprintf("%07o", entry.Mode), "oid": hash, "size": size}
							}
						}
					case "/xrpc/org.tangled.temp.git.getBlob":
						if r.URL.Query().Get("ref") != base.String() || r.URL.Query().Get("path") != ".mycontext/skills.yaml" {
							t.Fatal("private blob read was not exact pinned policy")
						}
						return response(200, existing), nil
					default:
						t.Fatal(r.URL)
					}
					data, _ := json.Marshal(value)
					return response(200, data), nil
				}
				if r.URL.Host != "knot.example" || r.Header.Get("Authorization") != "Bearer push-jwt" {
					t.Fatal("unexpected private Git destination or authorization")
				}
				if strings.HasSuffix(r.URL.Path, "info/refs") {
					if r.URL.Query().Get("service") != "git-receive-pack" {
						t.Fatal("private Git read attempted")
					}
					return advertisedGit(t, "git-receive-pack", map[string]plumbing.Hash{"refs/heads/main": base}), nil
				}
				if !strings.HasSuffix(r.URL.Path, "git-receive-pack") {
					t.Fatal(r.URL)
				}
				pushes++
				update := packp.NewReferenceUpdateRequest()
				if err := update.Decode(r.Body); err != nil {
					t.Fatal(err)
				}
				if len(update.Commands) != 1 || update.Commands[0].Old != plumbing.ZeroHash || update.Commands[0].Name.String() != "refs/heads/"+change.Branch {
					t.Fatal("private write rewrote a branch")
				}
				if err := packfile.UpdateObjectStorage(store, update.Packfile); err != nil {
					t.Fatal(err)
				}
				updated, err := object.GetCommit(store, update.Commands[0].New)
				if err != nil {
					t.Fatal(err)
				}
				updatedTree, _ := updated.Tree()
				for _, entry := range entries {
					if entry.Name == ".mycontext" {
						continue
					}
					actual, err := updatedTree.FindEntry(entry.Name)
					if err != nil || actual.Hash != entry.Hash || actual.Mode != entry.Mode {
						t.Fatalf("untouched %s changed", entry.Name)
					}
				}
				policy, err := updatedTree.File(".mycontext/skills.yaml")
				if err != nil {
					t.Fatal(err)
				}
				content, _ := policy.Contents()
				if !strings.Contains(content, "custom: retained") {
					t.Fatal("existing policy metadata lost")
				}
				status := packp.ReportStatus{UnpackStatus: "ok", CommandStatuses: []*packp.CommandStatus{{ReferenceName: update.Commands[0].Name, Status: "ok"}}}
				var body bytes.Buffer
				status.Encode(&body)
				resp := response(200, body.Bytes())
				resp.Header.Set("Content-Type", "application/x-git-receive-pack-result")
				return resp, nil
			})
			c.ReadService = &ReadService{Origin: "https://bobbin.example", DID: bobbinDID}
			c.Tokens = tokenFunc(func(_ context.Context, actor, audience, method string) (string, error) {
				if actor != actorDID {
					t.Fatal("wrong source actor")
				}
				if audience == bobbinDID {
					return "bobbin-jwt", nil
				}
				if audience != "did:web:knot.example" || method != "org.tangled.git.receivePack" {
					t.Fatal("wrong push grant")
				}
				return "push-jwt", nil
			})
			result, err := c.WriteAuthenticatedMetadataBranch(context.Background(), change)
			if corrupt {
				if err == nil || pushes != 0 {
					t.Fatal("corrupt authenticated source tree was pushed")
				}
			} else if err != nil || pushes != 1 || result.Base != base.String() {
				t.Fatalf("authenticated private write %+v %v", result, err)
			}
		})
	}
}
