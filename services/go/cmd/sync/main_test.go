package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyWebhookDispatchPreservesRepoEventsAndRawAppSignature(t *testing.T) {
	body := `{ "signed": "raw bytes" }` + "\n"
	syncCalls, appCalls := 0, 0
	check := func(r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != body || r.Header.Get("X-Hub-Signature-256") != "sha256=signature" {
			t.Fatal("dispatch changed signed webhook")
		}
	}
	syncHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		syncCalls++
		check(r)
		if r.URL.Path != "/webhooks/github" {
			t.Fatal(r.URL.Path)
		}
		w.WriteHeader(204)
	})
	appHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appCalls++
		check(r)
		if r.URL.Path != "/webhooks/github-app" {
			t.Fatal(r.URL.Path)
		}
		w.WriteHeader(202)
	})
	handler := newSyncRouter(syncHandler, appHandler)
	for _, event := range []string{"push", "ping", "installation", "installation_repositories", "github_app_authorization"} {
		r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
		r.Header.Set("X-GitHub-Event", event)
		r.Header.Set("X-Hub-Signature-256", "sha256=signature")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 202
		if event == "push" || event == "ping" {
			want = 204
		}
		if w.Code != want {
			t.Fatal(event, w.Code)
		}
	}
	if syncCalls != 2 || appCalls != 3 {
		t.Fatal(syncCalls, appCalls)
	}
}
