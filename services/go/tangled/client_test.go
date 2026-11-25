package tangled

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const repoDID = "did:plc:abcdefghijklmnopqrstuvwx"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func clientFor(t *testing.T, f transportFunc) Client {
	t.Helper()
	dir := identity.NewMockDirectory()
	dir.Insert(identity.Identity{DID: syntax.DID(repoDID), Handle: syntax.HandleInvalid, Services: map[string]identity.ServiceEndpoint{"tangled_knot": {Type: "TangledKnot", URL: "https://knot.example"}}})
	return Client{Directory: dir, HTTP: &http.Client{Transport: f}, TempDir: t.TempDir()}
}
func response(status int, data []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}
}
func archive(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("---\nname: example\ndescription: Example\n---\nInstructions")
	if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestSnapshotPinsBranchAndUsesSafeExtraction(t *testing.T) {
	sha := strings.Repeat("a", 40)
	calls := 0
	data := archive(t, "mycontext/a/SKILL.md")
	c := clientFor(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "knot.example" || r.URL.Query().Get("repo") != repoDID || r.Header.Get("Authorization") != "" {
			t.Fatalf("wrong repository request: %s", r.URL)
		}
		switch r.URL.Path {
		case "/xrpc/" + branchMethod:
			if r.URL.Query().Get("name") != "feature/a" {
				t.Fatal(r.URL)
			}
			return response(200, []byte(`{"name":"feature/a","hash":"`+sha+`"}`)), nil
		case "/xrpc/" + archiveMethod:
			q := r.URL.Query()
			if q.Get("ref") != sha || q.Get("format") != "tar.gz" || q.Get("prefix") != "mycontext" {
				t.Fatal(r.URL)
			}
			return response(200, data), nil
		default:
			t.Fatal(r.URL)
			return nil, errors.New("unexpected request")
		}
	})
	out, err := c.Snapshot(context.Background(), repoDID, "feature/a")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if calls != 2 || out.ResolvedCommitSHA != sha {
		t.Fatalf("unresolved snapshot: %+v calls=%d", out, calls)
	}
	if _, err := os.Stat(filepath.Join(out.RepositoryRoot, "a/SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
func TestBranchResolutionRejectsInvalidOrMismatchedResponse(t *testing.T) {
	for _, body := range []string{`{"name":"main","hash":"main"}`, `{"name":"other","hash":"` + strings.Repeat("a", 40) + `"}`, `{`} {
		c := clientFor(t, func(*http.Request) (*http.Response, error) { return response(200, []byte(body)), nil })
		if _, err := c.ResolveBranch(context.Background(), repoDID, "main"); !errors.Is(err, ErrInvalidRevision) {
			t.Fatalf("invalid branch accepted: %s %v", body, err)
		}
	}
}
func TestSnapshotRejectsOversizeAndTraversal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		limit int64
		want  error
	}{
		{"oversize", archive(t, "mycontext/a/SKILL.md"), 8, reposync.ErrArchiveTooLarge},
		{"traversal", archive(t, "../outside"), 1 << 20, reposync.ErrUnsafeEntry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/xrpc/"+branchMethod {
					return response(200, []byte(`{"name":"main","hash":"`+strings.Repeat("a", 40)+`"}`)), nil
				}
				return response(200, tc.data), nil
			})
			c.Limits.ArchiveBytes = tc.limit
			if _, err := c.Snapshot(context.Background(), repoDID, "main"); !errors.Is(err, tc.want) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
			entries, err := os.ReadDir(c.TempDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed extraction left files: %v %v", entries, err)
			}
		})
	}
}
func TestRepositoryResolutionRejectsUnsafeDIDService(t *testing.T) {
	for _, endpoint := range []string{"http://knot.example", "https://127.0.0.1", "https://10.0.0.1", "https://localhost", "https://knot.internal", "https://knot.example:8443", "https://user:pass@knot.example", "https://knot.example/path", "https://knot.example?x=y"} {
		t.Run(endpoint, func(t *testing.T) {
			c := clientFor(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe service reached network")
				return nil, errors.New("unexpected")
			})
			c.Directory.(*identity.MockDirectory).Insert(identity.Identity{DID: syntax.DID(repoDID), Handle: syntax.HandleInvalid, Services: map[string]identity.ServiceEndpoint{"tangled_knot": {Type: "TangledKnot", URL: endpoint}}})
			if _, err := c.ResolveRepository(context.Background(), repoDID); err == nil {
				t.Fatal("unsafe knot accepted")
			}
		})
	}
}
func TestSnapshotDenialDoesNotRetryAnonymouslyOrFetchArchive(t *testing.T) {
	calls := 0
	c := clientFor(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(403, []byte(`{"error":"Forbidden"}`)), nil
	})
	_, err := c.Snapshot(context.Background(), repoDID, "main")
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 403 || calls != 1 {
		t.Fatalf("denial failed closed: %v calls=%d", err, calls)
	}
}
func TestPinnedSchemasMatchImplementedMethods(t *testing.T) {
	for _, tc := range []struct{ file, id, kind string }{{"getBranch.json", branchMethod, "query"}, {"getArchive.json", archiveMethod, "query"}, {"submitPatch.json", "org.tangled.review.submitPatch", "procedure"}} {
		data, err := os.ReadFile(filepath.Join("upstream", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			ID   string `json:"id"`
			Defs map[string]struct {
				Type string `json:"type"`
			} `json:"defs"`
		}
		if json.Unmarshal(data, &schema) != nil || schema.ID != tc.id || schema.Defs["main"].Type != tc.kind {
			t.Fatalf("pinned schema mismatch: %s", tc.file)
		}
	}
}
