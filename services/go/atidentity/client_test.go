package atidentity

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/util/ssrf"
)

func testClientConfig(t *testing.T) ClientConfig {
	t.Helper()
	key, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatal(err)
	}
	return ClientConfig{ClientID: "https://mycontextprotocol.dev/oauth-client-metadata.json", CallbackURL: "https://mycontextprotocol.dev/auth/atproto/callback", PrivateKey: key, KeyID: "oauth-1", Collections: []string{"dev.mycontextprotocol.project.record"}}
}

func TestClientMetadataAndNetworkPolicy(t *testing.T) {
	c := testClientConfig(t)
	app, metadata, err := NewClient(c, oauth.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(c.ClientID); err != nil {
		t.Fatal(err)
	}
	if !metadata.DPoPBoundAccessTokens || !metadata.IsConfidential() || metadata.JWKS == nil || len(metadata.JWKS.Keys) != 1 || strings.Contains(metadata.Scope, "transition:") || strings.Contains(metadata.Scope, "*") {
		t.Fatalf("unsafe client metadata: %+v", metadata)
	}
	for _, action := range []string{"create", "update", "delete"} {
		if !strings.Contains(metadata.Scope, "repo:dev.mycontextprotocol.project.record?action="+action) {
			t.Fatalf("missing collection action %s", action)
		}
	}
	if app.Client != app.Resolver.Client || app.Client.CheckRedirect == nil || app.Client.Timeout == 0 {
		t.Fatal("discovery client bypasses network policy")
	}
	dir, ok := app.Dir.(*identity.BaseDirectory)
	if !ok || dir.HTTPClient.Transport != app.Client.Transport || dir.PLCClient != app.Client {
		t.Fatal("identity clients bypass network policy")
	}
	transport, ok := app.Client.Transport.(publicTransport)
	if !ok || transport.inner.(*http.Transport).Proxy != nil {
		t.Fatal("OAuth client inherits environment proxy")
	}
	if app.Client.CheckRedirect(nil, nil) == nil {
		t.Fatal("redirect was permitted")
	}
}

func TestDynamicRepositoryAudiencesStayBoundToReviewedMethods(t *testing.T) {
	c := testClientConfig(t)
	c.ServiceAuth = []ServicePermission{{Audience: "*", Method: "org.tangled.git.receivePack"}, {Audience: "*", Method: "org.tangled.temp.git.getTree"}}
	_, metadata, err := NewClient(c, oauth.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range strings.Fields(metadata.Scope) {
		p, err := auth.ParsePermissionString(scope)
		if err == nil && p.Resource == "rpc" && p.Audience == "*" && len(p.Endpoint) == 1 && p.Endpoint[0] == "org.tangled.git.receivePack" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing method-limited repository grant")
	}
	c.ServiceAuth = []ServicePermission{{Audience: "*", Method: "dev.mycontextprotocol.admin.updateAccountFlags"}}
	if _, _, err := NewClient(c, oauth.NewMemStore()); err == nil {
		t.Fatal("platform audience wildcard accepted")
	}
}

func TestClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ClientConfig)
	}{
		{"public client", func(c *ClientConfig) { c.PrivateKey = nil }},
		{"missing key ID", func(c *ClientConfig) { c.KeyID = "" }},
		{"wildcard permission", func(c *ClientConfig) { c.Collections = []string{"dev.mycontextprotocol.*"} }},
		{"injected scope", func(c *ClientConfig) { c.Collections = []string{"dev.mycontextprotocol.record?foo=bar"} }},
		{"insecure client", func(c *ClientConfig) { c.ClientID = "http://mycontextprotocol.dev/metadata.json" }},
		{"private callback", func(c *ClientConfig) { c.CallbackURL = "https://127.0.0.1/auth/callback" }},
		{"callback origin", func(c *ClientConfig) { c.CallbackURL = "https://other.example/auth/callback" }},
		{"client query", func(c *ClientConfig) { c.ClientID += "?secret=1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testClientConfig(t)
			test.change(&c)
			if _, _, err := NewClient(c, oauth.NewMemStore()); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	if _, _, err := NewClient(testClientConfig(t), nil); err == nil {
		t.Fatal("missing store accepted")
	}
}

type forbiddenTransport struct{ t *testing.T }

func (f forbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("unsafe request reached underlying transport")
	return nil, errors.New("unexpected transport call")
}
func TestPublicTransportRejectsUnsafeEndpointsBeforeNetwork(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/token", "https://127.0.0.1/token", "https://10.0.0.1/token", "https://169.254.169.254/latest/meta-data", "https://100.64.0.1/token", "https://[::1]/token", "https://[fc00::1]/token", "https://localhost/token", "https://account.local/token", "https://service.internal/token", "https://example.com:8443/token", "https://user:pass@example.com/token", "https://example.com/token#fragment",
	} {
		t.Run(raw, func(t *testing.T) {
			req, err := http.NewRequest("GET", raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (publicTransport{inner: forbiddenTransport{t}}).RoundTrip(req); err == nil {
				t.Fatal("unsafe endpoint accepted")
			}
		})
	}
}

func TestSocketPolicyRejectsResolvedPrivateAddress(t *testing.T) {
	client := publicHTTPClient()
	transport := client.Transport.(publicTransport).inner.(*http.Transport)
	if transport.DialContext == nil {
		t.Fatal("missing SSRF-safe dialer")
	}
	// Validate actual resolved socket addresses to cover public names rebinding.
	for _, address := range []string{"127.0.0.1:443", "10.0.0.1:443", "169.254.169.254:443", "[::1]:443", "8.8.8.8:22"} {
		if err := ssrf.PublicOnlyControl("tcp4", address, nil); err == nil {
			t.Fatalf("unsafe resolved address accepted: %s", address)
		}
	}
}

func TestServiceAuthScopesAreExactAndProtocolValid(t *testing.T) {
	config := testClientConfig(t)
	config.ServiceAuth = []ServicePermission{{Audience: "did:web:mycontextprotocol.dev#mycontextprotocol", Method: "dev.mycontextprotocol.project.listProjects"}}
	_, metadata, err := NewClient(config, oauth.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range strings.Fields(metadata.Scope) {
		if strings.HasPrefix(scope, "rpc:") {
			permission, e := auth.ParsePermissionString(scope)
			if e != nil || permission.Audience != config.ServiceAuth[0].Audience || len(permission.Endpoint) != 1 || permission.Endpoint[0] != config.ServiceAuth[0].Method {
				t.Fatalf("invalid service scope %s: %v", scope, e)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing service permission")
	}
	for _, bad := range []ServicePermission{{Audience: "*", Method: "dev.mycontextprotocol.project.listProjects"}, {Audience: "did:web:mycontextprotocol.dev", Method: "*"}, {Audience: "", Method: "dev.mycontextprotocol.project.listProjects"}, {Audience: "not-a-did", Method: "dev.mycontextprotocol.project.listProjects"}} {
		config.ServiceAuth = []ServicePermission{bad}
		if _, _, err := NewClient(config, oauth.NewMemStore()); err == nil {
			t.Fatalf("unsafe permission accepted: %+v", bad)
		}
	}
}
