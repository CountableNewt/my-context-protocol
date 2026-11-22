package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedGitHubCallbackKeepsInstallationAndLoginStatesSeparate(t *testing.T) {
	loginCalls, appCalls := 0, 0
	login := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { loginCalls++; w.WriteHeader(204) })
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { appCalls++; w.WriteHeader(202) })
	handler := newIdentityRouter(login, app, nil)
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/auth/github/callback?code=install&installation_id=7&state=E21A4021-D807-4E6A-81D2-3811C93939C6", 202},
		{"/auth/github/callback?code=install&state=e21a4021-d807-4e6a-81d2-3811c93939c6", 202},
		{"/auth/github/callback?code=login&state=eyJrIjoiZ2hfb2F1dGhfX2EifQ.signature", 204},
		{"/auth/github/callback?code=login&installation_id=7&state=signed.payload", 204},
		{"/auth/github/callback?code=login", 204},
		{"/auth/github/app/callback?code=install&state=nonce", 202},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", tc.target, nil)
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("route %s status%d want%d", tc.target, w.Code, tc.want)
		}
	}
	if appCalls != 3 || loginCalls != 3 {
		t.Fatal(appCalls, loginCalls)
	}
}
