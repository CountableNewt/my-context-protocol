package tangled

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
)

const actorDID = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
const bobbinDID = "did:web:api.tangled.example"

type tokenFunc func(context.Context, string, string, string) (string, error)

func (f tokenFunc) Token(ctx context.Context, actor, audience, method string) (string, error) {
	return f(ctx, actor, audience, method)
}
func authenticatedClient(t *testing.T, f transportFunc) Client {
	c := clientFor(t, f)
	c.ReadService = &ReadService{Origin: "https://bobbin.example", DID: bobbinDID, ArchiveMirrorOrigin: "https://mirror.example"}
	c.Tokens = tokenFunc(func(_ context.Context, actor, audience, method string) (string, error) {
		if actor != actorDID || audience != bobbinDID || !strings.HasPrefix(method, "org.tangled.") {
			t.Fatalf("invalid token audience %s %s %s", actor, audience, method)
		}
		return "service-token", nil
	})
	return c
}
func TestAuthenticatedSnapshotDoesNotForwardServiceJWTToMirror(t *testing.T) {
	sha := strings.Repeat("a", 40)
	count := 0
	c := authenticatedClient(t, func(r *http.Request) (*http.Response, error) {
		count++
		if r.URL.Host == "mirror.example" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("service credentials crossed mirror boundary")
			}
			return response(200, archive(t, "mycontext/SKILL.md")), nil
		}
		if r.Header.Get("Authorization") != "Bearer service-token" {
			t.Fatal("missing method bound service JWT")
		}
		if strings.HasSuffix(r.URL.Path, "getBranch") {
			return response(200, []byte(`{"name":"main","hash":"`+sha+`"}`)), nil
		}
		if r.URL.Query().Get("ref") != sha {
			t.Fatal("archive not pinned")
		}
		out := response(302, nil)
		out.Header.Set("Location", "https://mirror.example"+r.URL.RequestURI())
		return out, nil
	})
	out, err := c.AuthenticatedSnapshot(context.Background(), actorDID, repoDID, "main")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if count != 3 || out.ResolvedCommitSHA != sha {
		t.Fatal("unexpected archive sequence")
	}
}
func TestAuthenticatedReadRejectsDenialWithoutAnonymousRetry(t *testing.T) {
	calls := 0
	c := authenticatedClient(t, func(*http.Request) (*http.Response, error) { calls++; return response(401, nil), nil })
	if _, err := c.AuthenticatedSnapshot(context.Background(), actorDID, repoDID, "main"); err == nil {
		t.Fatal("denial accepted")
	}
	if calls != 1 {
		t.Fatal("denial retried anonymously")
	}
}
func TestAuthenticatedArchiveRejectsUnreviewedRedirect(t *testing.T) {
	for _, location := range []string{
		"https://evil.example/xrpc/org.tangled.temp.git.getArchive",
		"http://mirror.example/xrpc/org.tangled.temp.git.getArchive",
		"https://mirror.example/xrpc/org.tangled.temp.git.getArchive?repo=other",
		"https://mirror.example/other",
	} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			c := authenticatedClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return response(200, []byte(`{"name":"main","hash":"`+strings.Repeat("a", 40)+`"}`)), nil
				}
				if calls > 2 {
					t.Fatal("unsafe redirect followed")
				}
				out := response(302, nil)
				out.Header.Set("Location", location)
				return out, nil
			})
			if _, err := c.AuthenticatedSnapshot(context.Background(), actorDID, repoDID, "main"); err == nil {
				t.Fatal("unreviewed redirect accepted")
			}
		})
	}
}
func TestLookupChecksManifestOwnerAndNativeIdentity(t *testing.T) {
	for _, owner := range []string{actorDID, "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"} {
		c := authenticatedClient(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("owner") != actorDID || r.URL.Query().Get("name") != "repo" {
				t.Fatal(r.URL)
			}
			return response(200, []byte(`{"uri":"at://`+repoDID+`/org.tangled.repo.manifest/self","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","value":{"$type":"org.tangled.repo.manifest","declaration":{"owner":"`+owner+`","name":"repo"}},"git":{"defaultBranch":"main"}}`)), nil
		})
		view, err := c.LookupRepository(context.Background(), actorDID, actorDID, "repo")
		if owner == actorDID {
			if err != nil || view.RepositoryDID != repoDID {
				t.Fatalf("valid manifest rejected %+v %v", view, err)
			}
		} else if err == nil {
			t.Fatal("spoofed declaration owner accepted")
		}
	}
}

func TestListRepositoriesBoundsPagesAndValidatesOwner(t *testing.T) {
	calls := 0
	c := authenticatedClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("subject") != actorDID || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("cursor") != "next" {
			t.Fatal("repository page parameters changed")
		}
		return response(200, []byte(`{"items":[{"uri":"at://`+repoDID+`/org.tangled.repo.manifest/self","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","value":{"$type":"org.tangled.repo.manifest","declaration":{"owner":"`+actorDID+`","name":"repo"}}}],"cursor":"last"}`)), nil
	})
	page, err := c.ListRepositories(context.Background(), actorDID, actorDID, "next", 1)
	if err != nil || len(page.Repositories) != 1 || page.Cursor != "last" || page.Repositories[0].RepositoryDID != repoDID {
		t.Fatalf("invalid public discovery page %+v %v", page, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := c.ListRepositories(context.Background(), actorDID, actorDID, "", limit); err == nil {
			t.Fatal("unbounded repository page accepted")
		}
	}
	if calls != 1 {
		t.Fatal("invalid request reached external service")
	}
}

type bindingFunc func(context.Context, string) (Binding, error)

func (f bindingFunc) ResolveTangledConnection(ctx context.Context, id string) (Binding, error) {
	return f(ctx, id)
}
func TestProviderUsesNativeBindingsAndChecksChangedHead(t *testing.T) {
	sha := strings.Repeat("a", 40)
	c := authenticatedClient(t, func(r *http.Request) (*http.Response, error) {
		return response(200, []byte(`{"name":"main","hash":"`+sha+`"}`)), nil
	})
	p := Provider{Client: c, Connections: bindingFunc(func(_ context.Context, id string) (Binding, error) {
		if id != "connection" {
			return Binding{}, errors.New("missing")
		}
		return Binding{RepositoryDID: repoDID, OwnerDID: actorDID, ActorDID: actorDID, Branch: "main", Private: true}, nil
	})}
	connection := syncservice.Connection{Provider: "tangled", ID: "connection", Owner: "wrong", Repository: "wrong", AccountEncryptedToken: "github-secret"}
	changed, head, err := p.Changed(context.Background(), connection, strings.Repeat("b", 40))
	if err != nil || !changed || head != sha {
		t.Fatalf("native binding resolution: %v %s %v", changed, head, err)
	}
	changed, _, err = p.Changed(context.Background(), connection, sha)
	if err != nil || changed {
		t.Fatal("same revision scheduled again")
	}
	connection.Provider = "github"
	if _, err := p.ResolveRepositoryCommit(context.Background(), connection); err == nil {
		t.Fatal("cross-provider binding accepted")
	}
}

func TestBobbinServiceReferenceAudiencePreserved(t *testing.T) {
	c := authenticatedClient(t, func(*http.Request) (*http.Response, error) { return response(200, []byte(`{}`)), nil })
	exact := bobbinDID + "#tangled_bobbin"
	c.ReadService.DID = exact
	calls := 0
	c.Tokens = tokenFunc(func(_ context.Context, actor, audience, method string) (string, error) {
		calls++
		if audience != exact {
			t.Fatalf("audience rewritten: %s", audience)
		}
		return "scoped", nil
	})
	resp, err := c.serviceRequest(context.Background(), actorDID, "org.tangled.temp.repo.getRepoByName", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, invalid := range []string{bobbinDID + "#", bobbinDID + "#one#two", bobbinDID + "#service/path", bobbinDID + "#bad?query"} {
		c.ReadService.DID = invalid
		if _, err := c.serviceRequest(context.Background(), actorDID, "org.tangled.temp.repo.getRepoByName", nil); err == nil {
			t.Fatalf("invalid audience accepted: %s", invalid)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid reference minted tokens: %d", calls)
	}
}
