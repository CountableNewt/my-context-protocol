package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstallationValidationAndPrivateSave(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-team", "extra-scope", "all-projects", "redirect", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			var saves []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/oauth/access_token" {
					if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("client_secret") != "private" || r.Form.Get("code") != "code" || r.Form.Get("redirect_uri") != "http://"+host+"/callback/test" {
						t.Error("invalid code exchange")
					}
					if mode == "redirect" {
						w.Header().Set("Location", "/private")
						w.WriteHeader(302)
						return
					}
					team := teamID
					if mode == "wrong-team" {
						team = "other"
					}
					fmt.Fprintf(w, `{"access_token":"private-token","token_type":"Bearer","installation_id":"icfg_test","team_id":%q}`, team)
					return
				}
				if r.URL.Path != "/v1/integrations/configuration/icfg_test" || r.URL.Query().Get("teamId") != teamID || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Error("invalid configuration check")
				}
				scopes := []string{"read:integration-configuration", "read-write:project", "read-write:domain"}
				if mode == "extra-scope" {
					scopes = append(scopes, "read-write:global-project-env-vars")
				}
				selection := "selected"
				if mode == "all-projects" {
					selection = "all"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "icfg_test", "teamId": teamID, "projects": []string{projectID}, "projectSelection": selection, "scopes": scopes})
			}))
			defer server.Close()
			h := &callback{credentials: credentials{"client", "private"}, path: "/callback/test", api: server.URL, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, save: func(ctx context.Context, name, value string) error {
				saves = append(saves, name)
				if strings.Contains(name, "SECRET") && value != "private" {
					t.Error("wrong secret")
				}
				if mode == "save-failure" {
					return safeError()
				}
				return nil
			}}
			e := h.install(context.Background(), "code")
			if mode == "valid" {
				if e != nil || len(saves) != 3 {
					t.Fatal("valid installation failed", e)
				}
			} else {
				if e == nil || strings.Contains(e.Error(), "private") {
					t.Fatal("invalid installation accepted or leaked", e)
				}
				if mode != "save-failure" && len(saves) != 0 {
					t.Fatal("unvalidated credentials saved")
				}
			}
		})
	}
}

func TestCallbackRejectsWrongHostPathPeerAndDuplicateCode(t *testing.T) {
	for _, mode := range []string{"host", "path", "peer", "method", "duplicate-code"} {
		t.Run(mode, func(t *testing.T) {
			h := &callback{path: "/callback/private"}
			r := httptest.NewRequest("GET", "http://"+host+h.path+"?code=one", nil)
			r.RemoteAddr = "127.0.0.1:12000"
			switch mode {
			case "host":
				r.Host = "evil.example"
			case "path":
				r.URL.Path = "/callback/other"
			case "peer":
				r.RemoteAddr = "192.0.2.1:12000"
			case "method":
				r.Method = "POST"
			case "duplicate-code":
				r.URL.RawQuery = "code=one&code=two"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 || h.used {
				t.Fatal("unsafe callback accepted")
			}
			if w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("callback leakage protection missing")
			}
		})
	}
	h := &callback{path: "/callback/private", used: true}
	r := httptest.NewRequest("GET", "http://"+host+h.path+"?code=one", nil)
	r.RemoteAddr = "127.0.0.1:12000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("replay accepted")
	}
}

func TestAllowedScopesExactMandatoryReadAndWrites(t *testing.T) {
	valid := []string{"read:integration-configuration", "read-write:project", "read-write:domain"}
	if !allowedScopes(valid) {
		t.Fatal("mandatory read rejected")
	}
	for _, s := range [][]string{valid[:2], append(append([]string{}, valid...), "read-write:integration-configuration"), append(append([]string{}, valid...), valid[0]), {"read:integration-configuration", "write:project", "read-write:domain"}} {
		if allowedScopes(s) {
			t.Fatal("unexpected scopes accepted")
		}
	}
}
