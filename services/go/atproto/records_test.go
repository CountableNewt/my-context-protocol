package atproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bluesky-social/indigo/atproto/syntax"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type authorizerFunc func(context.Context, *http.Request, string) error

func (f authorizerFunc) Authorize(ctx context.Context, r *http.Request, did string) error {
	return f(ctx, r, did)
}
func mutation() Mutation {
	return Mutation{Repo: "did:plc:alice", Collection: "dev.mycontextprotocol.project", RKey: StableKey("dev.mycontextprotocol.project", "legacy"), Record: json.RawMessage(`{"$type":"dev.mycontextprotocol.project","name":"Example"}`)}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClientCASAndAuth(t *testing.T) {
	m := mutation()
	calls := 0
	c, err := NewClient("https://pds.example", transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "pds.example" || r.Header.Get("Authorization") != "DPoP test" {
			t.Fatal("missing scoped authorization")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["swapRecord"]) != "null" {
			t.Fatalf("create must assert absence: %s", body["swapRecord"])
		}
		return response(200, `{"uri":"`+m.URI()+`","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), nil
	}), authorizerFunc(func(_ context.Context, r *http.Request, did string) error {
		if did != m.Repo {
			t.Fatal("wrong owner")
		}
		r.Header.Set("Authorization", "DPoP test")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.Put(context.Background(), m)
	if err != nil || ref.CID != "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || calls != 1 {
		t.Fatalf("%+v %v", ref, err)
	}
}

func TestClientRejectsRedirectAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		conflict bool
	}{{"redirect", 302, `{}`, false}, {"bad json", 200, `oops`, false}, {"wrong reference", 200, `{"uri":"at://wrong","cid":"x"}`, false}, {"conflict", 400, `{"error":"InvalidSwap","message":"secret"}`, true}, {"oversized", 200, strings.Repeat("x", (1<<20)+1), false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, _ := NewClient("https://pds.example", transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				res := response(tc.status, tc.body)
				res.Header.Set("Location", "https://attacker.example")
				return res, nil
			}), authorizerFunc(func(context.Context, *http.Request, string) error { return nil }))
			_, err := c.Put(context.Background(), mutation())
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tc.conflict && !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked body")
			}
		})
	}
}

type memoryJournal struct {
	mu           sync.Mutex
	digests      map[string]string
	refs         map[string]Reference
	failComplete bool
}

func (j *memoryJournal) Reserve(_ context.Context, owner, key, digest string, _ Mutation) (*Reference, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	key = owner + "/" + key
	if old := j.digests[key]; old != "" && old != digest {
		return nil, ErrConflict
	}
	j.digests[key] = digest
	if ref, ok := j.refs[key]; ok {
		return &ref, nil
	}
	return nil, nil
}
func (j *memoryJournal) Complete(_ context.Context, owner, key, digest string, ref Reference) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failComplete {
		j.failComplete = false
		return errors.New("journal unavailable")
	}
	key = owner + "/" + key
	if j.digests[key] != digest {
		return ErrConflict
	}
	j.refs[key] = ref
	return nil
}

type memoryRecords struct {
	mu           sync.Mutex
	record       *Record
	puts         int
	loseResponse bool
	failure      error
}

func (s *memoryRecords) Get(_ context.Context, _ Mutation) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return Record{}, s.failure
	}
	if s.record == nil {
		return Record{}, ErrNotFound
	}
	return *s.record, nil
}
func (s *memoryRecords) Put(_ context.Context, m Mutation) (Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record != nil && (m.ExpectedCID == nil || *m.ExpectedCID != s.record.CID) {
		return Reference{}, ErrConflict
	}
	if s.record == nil && m.ExpectedCID != nil {
		return Reference{}, ErrConflict
	}
	s.puts++
	ref := Reference{m.URI(), "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	s.record = &Record{ref, append(json.RawMessage(nil), m.Record...)}
	if s.loseResponse {
		s.loseResponse = false
		return Reference{}, errors.New("connection reset after commit")
	}
	return ref, nil
}
func publisher() (Publisher, *memoryRecords, *memoryJournal) {
	s := &memoryRecords{}
	j := &memoryJournal{digests: map[string]string{}, refs: map[string]Reference{}}
	return Publisher{s, j}, s, j
}

func TestPublicationRecoversLostAcknowledgements(t *testing.T) {
	for _, failure := range []string{"PDS response", "journal response"} {
		t.Run(failure, func(t *testing.T) {
			p, s, j := publisher()
			if failure == "PDS response" {
				s.loseResponse = true
			} else {
				j.failComplete = true
			}
			m := mutation()
			if _, err := p.Publish(context.Background(), m.Repo, "request", m); err == nil {
				t.Fatal("must not report completion")
			}
			ref, err := p.Publish(context.Background(), m.Repo, "request", m)
			if err != nil || ref.CID == "" || s.puts != 1 {
				t.Fatalf("%+v %v writes=%d", ref, err, s.puts)
			}
			if _, err = p.Publish(context.Background(), m.Repo, "request", m); err != nil || s.puts != 1 {
				t.Fatal("completed retry wrote again")
			}
		})
	}
}

func TestPublicationRejectsChangedIntentAndForeignOwner(t *testing.T) {
	p, s, _ := publisher()
	m := mutation()
	if _, err := p.Publish(context.Background(), "did:plc:other", "request", m); err == nil {
		t.Fatal("foreign owner allowed")
	}
	if _, err := p.Publish(context.Background(), m.Repo, "request", m); err != nil {
		t.Fatal(err)
	}
	m.Record = json.RawMessage(`{"$type":"dev.mycontextprotocol.project","name":"Changed"}`)
	if _, err := p.Publish(context.Background(), m.Repo, "request", m); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if s.puts != 1 {
		t.Fatal("conflicting intent wrote")
	}
}

func TestPublicationConcurrentRetries(t *testing.T) {
	p, s, _ := publisher()
	m := mutation()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Publish(context.Background(), m.Repo, "request", m)
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := p.Publish(context.Background(), m.Repo, "request", m); err != nil {
		t.Fatal(err)
	}
	if s.puts != 1 {
		t.Fatalf("writes=%d", s.puts)
	}
}

func TestPublicationPDSOutageDoesNotComplete(t *testing.T) {
	p, s, j := publisher()
	s.failure = errors.New("PDS offline")
	m := mutation()
	if _, err := p.Publish(context.Background(), m.Repo, "request", m); err == nil {
		t.Fatal("outage succeeded")
	}
	if len(j.refs) != 0 || s.puts != 0 {
		t.Fatal("completed during outage")
	}
}

func TestClientUpdateCarriesExpectedCID(t *testing.T) {
	m := mutation()
	cid := "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m.ExpectedCID = &cid
	c, _ := NewClient("https://pds.example", transportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["swapRecord"]) != `"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"` {
			t.Fatalf("missing update precondition: %s", body["swapRecord"])
		}
		return response(400, `{"error":"InvalidSwap"}`), nil
	}), authorizerFunc(func(context.Context, *http.Request, string) error { return nil }))
	if _, err := c.Put(context.Background(), m); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestValidationAndOrigins(t *testing.T) {
	auth := authorizerFunc(func(context.Context, *http.Request, string) error { return nil })
	for _, origin := range []string{"http://pds.example", "https://user:pass@pds.example", "https://pds.example/path", "https://pds.example?token=secret", "https://pds.example#fragment"} {
		if _, err := NewClient(origin, nil, auth); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	for _, change := range []func(*Mutation){
		func(m *Mutation) { m.Repo = "did:plc:alice/else" },
		func(m *Mutation) { m.Collection = "project" },
		func(m *Mutation) { m.RKey = "../escape" },
		func(m *Mutation) { m.RKey = ".." },
		func(m *Mutation) { m.Record = json.RawMessage(`{"$type":"other.namespace.record"}`) },
		func(m *Mutation) { m.Record = json.RawMessage(`null`) },
		func(m *Mutation) { empty := ""; m.ExpectedCID = &empty },
	} {
		m := mutation()
		change(&m)
		if err := m.Validate(); err == nil {
			t.Fatalf("accepted invalid mutation %+v", m)
		}
	}
	if StableKey("a", "bc") == StableKey("ab", "c") {
		t.Fatal("ambiguous stable key boundary")
	}
}

type authenticatedFunc func(context.Context, *http.Request, string, syntax.NSID) (*http.Response, error)

func (f authenticatedFunc) DoWithAuth(ctx context.Context, r *http.Request, repo string, method syntax.NSID) (*http.Response, error) {
	return f(ctx, r, repo, method)
}

func TestAuthenticatedExecutorReceivesOwnerAndBoundedContext(t *testing.T) {
	m := mutation()
	c, err := NewAuthenticatedClient("https://pds.example", authenticatedFunc(func(ctx context.Context, r *http.Request, repo string, method syntax.NSID) (*http.Response, error) {
		if repo != m.Repo || method.String() != "com.atproto.repo.putRecord" || r.URL.Host != "pds.example" {
			t.Fatal("wrong authorization destination")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded authenticated call")
		}
		return response(200, `{"uri":"`+m.URI()+`","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Put(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizerCannotRedirectCredentials(t *testing.T) {
	called := false
	c, _ := NewClient("https://pds.example", transportFunc(func(*http.Request) (*http.Response, error) { called = true; return response(200, `{}`), nil }), authorizerFunc(func(_ context.Context, r *http.Request, _ string) error {
		r.URL.Host = "attacker.example"
		r.Header.Set("Authorization", "secret")
		return nil
	}))
	if _, err := c.Put(context.Background(), mutation()); err == nil || called {
		t.Fatal("authorizer changed destination")
	}
}
