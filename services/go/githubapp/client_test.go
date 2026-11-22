package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

var keyOnce sync.Once
var testKey *rsa.PrivateKey

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		testKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
	})
	return testKey
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestAppJWTAndPrivateKeyNormalization(t *testing.T) {
	k := key(t)
	raw := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	for _, tc := range []struct{ raw, encoded string }{{raw, ""}, {strings.ReplaceAll(raw, "\n", `\n`), ""}, {"\ufeff\"" + strings.ReplaceAll(raw, "\n", "") + "\"", ""}, {"", base64.RawURLEncoding.EncodeToString([]byte(raw))}} {
		parsed, err := LoadPrivateKey(tc.raw, tc.encoded)
		if err != nil || parsed.N.Cmp(k.N) != 0 {
			t.Fatalf("key normalization failed %v", err)
		}
	}
	now := time.Unix(1800000000, 0)
	client := Client{ClientID: "Iv1-client", Key: k}
	jwt, err := client.AppJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	data, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(data, &claims)
	if claims["iss"] != "Iv1-client" || claims["iat"] != float64(now.Unix()-60) || claims["exp"] != float64(now.Unix()+600) {
		t.Fatal(claims)
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, hash[:], signature) != nil {
		t.Fatal("invalid JWT signature")
	}
	if _, err = LoadPrivateKey("not PEM", ""); err == nil {
		t.Fatal("accepted invalid key")
	}
}
func TestInstallationTokenAndScopeFailures(t *testing.T) {
	calls := 0
	client := Client{ClientID: "client", Key: key(t), HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.URL.Path != "/app/installations/7/access_tokens" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Fatalf("bad request %s %s", r.URL, r.Header.Get("Authorization"))
		}
		if r.Method != "POST" {
			t.Fatal(r.Method)
		}
		return reply(201, `{"token":"installation-secret","expires_at":"2099-01-01T00:00:00Z"}`), nil
	})}}
	token, err := client.InstallationToken(context.Background(), "7", "oauth-ignored")
	if err != nil || token != "installation-secret" || calls != 1 {
		t.Fatalf("%s %v", token, err)
	}
	if _, err = client.InstallationToken(context.Background(), "7/../../evil", ""); err == nil || calls != 1 {
		t.Fatal("invalid id reached transport")
	}
	calls = 0
	client.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") == "Bearer installation" {
			return reply(404, `{}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer oauth" {
			t.Fatal("wrong fallback")
		}
		return reply(200, `{"name":"repo"}`), nil
	})
	_, err = client.VerifyRepository(context.Background(), "owner", "repo", "installation", "oauth")
	if !errors.Is(err, ErrInstallationScope) || calls != 2 {
		t.Fatalf("%v calls=%d", err, calls)
	}
}
func TestPaginationRejectsCredentialExfiltration(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		res := reply(200, `[]`)
		res.Header.Set("Link", `<https://evil.example/user/repos>; rel="next"`)
		return res, nil
	})}}
	if _, err := client.ListRepositories(context.Background(), "secret"); err == nil {
		t.Fatal("followed malicious next link")
	}
	client.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		res := reply(302, "")
		res.Header.Set("Location", "https://evil.example")
		return res, nil
	})
	if _, err := client.ListRepositories(context.Background(), "secret"); err == nil {
		t.Fatal("followed API redirect")
	}
}
func TestWebhookCreationAndBoundedResponses(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		config := body["config"].(map[string]any)
		if config["url"] != "https://app.example/webhooks/github" || len(config["secret"].(string)) != 64 || config["insecure_ssl"] != "0" {
			t.Fatal(config)
		}
		return reply(201, `{"id":123}`), nil
	})}}
	id, secret, err := client.CreateWebhook(context.Background(), "owner", "repo", "oauth", "https://app.example")
	if err != nil || id != "123" || len(secret) != 64 {
		t.Fatalf("%s %s %v", id, secret, err)
	}
	if _, _, err = client.CreateWebhook(context.Background(), "owner", "repo", "oauth", "http://app.example"); err == nil {
		t.Fatal("insecure webhook accepted")
	}
	client.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return reply(200, strings.Repeat("x", maxResponse+1)), nil
	})
	if _, err = client.ListRepositories(context.Background(), "oauth"); err == nil {
		t.Fatal("oversized response accepted")
	}
}
func TestOAuthExchangeUsesJSONAcceptAndUserIdentity(t *testing.T) {
	calls := 0
	client := Client{ClientID: "client", ClientSecret: "secret", SetupCallback: "https://app.example/auth/github/app/callback", HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Header.Get("Accept") != "application/json" || r.URL.Host != "github.com" {
				t.Fatal("OAuth must request JSON")
			}
			return reply(200, `{"access_token":"user-token"}`), nil
		}
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatal(r.URL)
		}
		return reply(200, `{"id":101}`), nil
	})}}
	token, id, err := client.ExchangeInstallOAuth(context.Background(), "code")
	if err != nil || token != "user-token" || id != 101 {
		t.Fatalf("%s %d %v", token, id, err)
	}
}

func TestRepositoryPaginationAndPermissionFields(t *testing.T) {
	calls := 0
	client := Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer oauth" {
			t.Fatal("missing repository credential")
		}
		res := reply(200, `[{"full_name":"owner/repo","name":"repo","owner":{"login":"owner"},"default_branch":"main","private":true,"permissions":{"admin":true,"push":true,"pull":true}}]`)
		if calls == 1 {
			res.Header.Set("Link", `<https://api.github.com/user/repos?page=2>; rel="next"`)
		}
		return res, nil
	})}}
	repos, err := client.ListRepositories(context.Background(), "oauth")
	if err != nil || calls != 2 || len(repos) != 2 || !repos[0].Permissions["admin"] || !repos[0].Private {
		t.Fatalf("%+v %v calls%d", repos, err, calls)
	}
}
