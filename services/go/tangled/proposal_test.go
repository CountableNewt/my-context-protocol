package tangled

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func proposal() PatchProposal {
	return PatchProposal{ActorDID: actorDID, RepositoryDID: repoDID, SourceRepositoryDID: repoDID, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), SourceBranch: "mycontext/metadata", TargetBranch: "main", Title: "Update runtime metadata", Body: "Apply checksum-bound runtime policy."}
}
func TestNativeProposalUsesRepoAudienceAndExistingGitObjects(t *testing.T) {
	calls := 0
	ticket := "at://" + repoDID + "/org.tangled.track.ticket/ticket1"
	c := clientFor(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer short-lived" {
			t.Fatal("unauthenticated native write")
		}
		var input map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Fatal("invalid JSON")
		}
		var addressed string
		json.Unmarshal(input["repo"], &addressed)
		if addressed != repoDID {
			t.Fatal("wrong repo audience")
		}
		if calls == 1 {
			var record map[string]any
			json.Unmarshal(input["record"], &record)
			if record["$type"] != "org.tangled.track.ticket" || record["state"] != "open" {
				t.Fatal(record)
			}
			return response(200, []byte(`{"uri":"`+ticket+`","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)), nil
		}
		if r.URL.Path != "/xrpc/org.tangled.review.submitPatch" {
			t.Fatal(r.URL)
		}
		var patch struct {
			Ticket string `json:"ticket"`
			Base   struct {
				OID string `json:"oid"`
			} `json:"base"`
			Head struct {
				OID string `json:"oid"`
			} `json:"head"`
			SourceRepo string `json:"sourceRepo"`
		}
		json.Unmarshal(input["patch"], &patch)
		if patch.Ticket != "ticket1" || patch.Base.OID != proposal().Base || patch.Head.OID != proposal().Head || patch.SourceRepo != repoDID {
			t.Fatalf("invalid patch %+v", patch)
		}
		return response(200, []byte(`{"uri":"at://`+repoDID+`/org.tangled.review.patch/patch1","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)), nil
	})
	c.Tokens = tokenFunc(func(_ context.Context, actor, audience, method string) (string, error) {
		if actor != actorDID || audience != repoDID || method != "com.atproto.repo.createRecord" && method != "org.tangled.review.submitPatch" {
			t.Fatal("invalid auth mint")
		}
		return "short-lived", nil
	})
	ref, err := c.CreateProposal(context.Background(), proposal())
	if err != nil || ref.PatchURI == "" || ref.TicketURI != ticket || calls != 2 {
		t.Fatalf("proposal %+v %v calls %d", ref, err, calls)
	}
}
func TestProposalFailureKeepsCreatedTicketForResume(t *testing.T) {
	calls := 0
	c := clientFor(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, []byte(`{"uri":"at://`+repoDID+`/org.tangled.track.ticket/ticket1","cid":"bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)), nil
		}
		return response(503, nil), nil
	})
	c.Tokens = tokenFunc(func(context.Context, string, string, string) (string, error) { return "jwt", nil })
	ref, err := c.CreateProposal(context.Background(), proposal())
	var failed *ProposalError
	if !errors.As(err, &failed) || failed.TicketURI == "" || ref.TicketURI != failed.TicketURI || ref.PatchURI != "" {
		t.Fatalf("lost external ticket: %+v %v", ref, err)
	}
}
func TestProposalRejectsInvalidInputBeforeExternalWrites(t *testing.T) {
	c := clientFor(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid proposal wrote externally")
		return nil, nil
	})
	p := proposal()
	p.Head = p.Base
	if _, err := c.CreateProposal(context.Background(), p); err == nil {
		t.Fatal("empty change accepted")
	}
}

type attestFunc func(context.Context, string, string, string, string) error

func (f attestFunc) Attest(ctx context.Context, actor, uri, cid, change string) error {
	return f(ctx, actor, uri, cid, change)
}
func TestProposalAttestationIsResumableWithoutNewKnotWrites(t *testing.T) {
	ref := ProposalReference{TicketURI: "at://" + repoDID + "/org.tangled.track.ticket/ticket1", TicketCID: "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PatchURI: "at://" + repoDID + "/org.tangled.review.patch/patch1", PatchCID: "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	calls := 0
	writer := attestFunc(func(_ context.Context, actor, uri, cid, change string) error {
		calls++
		if actor != actorDID || cid == "" {
			t.Fatal("author attribution lost")
		}
		if calls == 2 {
			return errors.New("PDS unavailable")
		}
		return nil
	})
	if err := AttestProposal(context.Background(), writer, actorDID, ref); err == nil {
		t.Fatal("failed PDS attestation reported success")
	}
	if err := AttestProposal(context.Background(), writer, actorDID, ref); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("resume did not retain both subject references")
	}
	ref.PatchURI = "invalid"
	calls = 0
	if err := AttestProposal(context.Background(), writer, actorDID, ref); err == nil || calls != 0 {
		t.Fatal("invalid subject published before validation")
	}
}
