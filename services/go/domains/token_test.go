package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntegrationTokenCacheExpiryAndConcurrency(t *testing.T) {
	s, _, _, _ := fixture(t)
	s.config.Token = ""
	s.config.IntegrationClientID = "client"
	s.config.IntegrationClientSecret = "private"
	s.config.IntegrationInstallationID = "icfg_test"
	now := time.Now()
	s.now = func() time.Time { return now }
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "client" || secret != "private" || r.URL.Path != "/v2/oauth/access_token" || r.ParseForm() != nil || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("installation_id") != "icfg_test" {
			t.Error("invalid token request")
		}
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"minted","token_type":"Bearer","expires_in":3600,"installation_id":"icfg_test","team_id":"team"}`)
	}))
	defer server.Close()
	s.api = server.URL
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, e := s.accessToken(context.Background())
			if e != nil || token != "minted" {
				t.Error("mint failed", e)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent mint count", calls.Load())
	}
	now = now.Add(3540 * time.Second)
	if _, e := s.accessToken(context.Background()); e != nil || calls.Load() != 2 {
		t.Fatal("expired cache retained", e)
	}
}

func TestIntegrationTokenFailuresAreRedactedAndFailClosed(t *testing.T) {
	for _, body := range []string{`{}`, `private broken JSON`, `{"access_token":"bad\nsecret","token_type":"Bearer","expires_in":3600,"installation_id":"icfg_test","team_id":"team"}`, `{"access_token":"private","token_type":"Bearer","expires_in":0,"installation_id":"icfg_test","team_id":"team"}`, `{"access_token":"private","token_type":"Bearer","expires_in":3600,"installation_id":"icfg_other","team_id":"team"}`, `{"access_token":"private","token_type":"Bearer","expires_in":3600,"installation_id":"icfg_test","team_id":"other"}`} {
		t.Run(fmt.Sprint(len(body), body[:1]), func(t *testing.T) {
			s, _, _, _ := fixture(t)
			s.config.Token = ""
			s.config.IntegrationInstallationID = "icfg_test"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			s.api = server.URL
			token, e := s.accessToken(context.Background())
			if e == nil || token != "" || strings.Contains(e.Error(), "private") {
				t.Fatal("invalid token accepted or leaked", e)
			}
		})
	}
	for _, status := range []int{302, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, _, _, _ := fixture(t)
			s.config.Token = ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/leak")
				w.WriteHeader(status)
				fmt.Fprint(w, "private")
			}))
			defer server.Close()
			s.api = server.URL
			if _, e := s.accessToken(context.Background()); e == nil || strings.Contains(e.Error(), "private") {
				t.Fatal("token error accepted", e)
			}
		})
	}
}

func TestDomain401InvalidatesTokenWithoutReplayingMutation(t *testing.T) {
	s, _, _, _ := fixture(t)
	s.config.Token = ""
	s.cachedToken = "minted"
	s.tokenExpiry = time.Now().Add(time.Hour)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
	defer server.Close()
	s.api = server.URL
	if e := s.call(context.Background(), "POST", "/domain", nil, nil); e == nil || calls != 1 || s.cachedToken != "" {
		t.Fatal("401 did not fail closed")
	}
}

func TestIntegrationConfigRejectsPartialOrMixedCredentials(t *testing.T) {
	s, _, _, _ := fixture(t)
	for _, c := range []Config{{Token: "static", IntegrationClientID: "client"}, {IntegrationClientID: "client"}, {IntegrationClientID: "client", IntegrationClientSecret: "secret", IntegrationInstallationID: "invalid"}} {
		c.ProjectID = "project"
		c.TeamID = "team"
		if _, e := New(s.db, c); e == nil {
			raw, _ := json.Marshal(c)
			t.Fatal("invalid config accepted", string(raw))
		}
	}
}

func TestIntegrationTokenCanceledAndTransportFailure(t *testing.T) {
	s, _, _, _ := fixture(t)
	s.config.Token = ""
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.accessToken(ctx); e == nil {
		t.Fatal("canceled mint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("closed server reached") }))
	s.api = server.URL
	server.Close()
	if _, e := s.accessToken(context.Background()); e == nil || strings.Contains(e.Error(), server.URL) {
		t.Fatal("transport error accepted or leaked", e)
	}
}
