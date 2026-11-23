package atidentity

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util/ssrf"
)

// ClientConfig configures the hosted confidential OAuth client. Collections are
// explicit NSIDs rather than broad transitional or wildcard permissions.
type ClientConfig struct {
	ClientID    string
	CallbackURL string
	Collections []string
	ServiceAuth []ServicePermission
	PrivateKey  atcrypto.PrivateKey
	KeyID       string
}

// ServicePermission grants one exact method at a service DID/ref. Tangled's
// reviewed methods may use a dynamic audience because repositories have their
// own DIDs, discovered only after login. Platform methods require an exact target.
type ServicePermission struct {
	Audience string `json:"audience"`
	Method   string `json:"method"`
}

// NewClient delegates PAR, PKCE, DPoP, and client assertions to the pinned Indigo
// SDK. Callers must publish the returned metadata at ClientID. The supplied store
// must durably encrypt OAuth authorization requests and sessions.
func NewClient(c ClientConfig, store oauth.ClientAuthStore) (*oauth.ClientApp, oauth.ClientMetadata, error) {
	var empty oauth.ClientMetadata
	if store == nil {
		return nil, empty, errors.New("durable OAuth store is required")
	}
	clientURL, err := publicURL(c.ClientID)
	if err != nil {
		return nil, empty, fmt.Errorf("invalid OAuth client ID: %w", err)
	}
	callback, err := publicURL(c.CallbackURL)
	if err != nil {
		return nil, empty, fmt.Errorf("invalid OAuth callback: %w", err)
	}
	if clientURL.RawQuery != "" || callback.RawQuery != "" || clientURL.Host != callback.Host {
		return nil, empty, errors.New("OAuth metadata and callback must share an origin and have no query")
	}
	if c.PrivateKey == nil || strings.TrimSpace(c.KeyID) == "" || strings.ContainsAny(c.KeyID, "\r\n") {
		return nil, empty, errors.New("hosted OAuth requires a P-256 assertion key and key ID")
	}
	scopes := []string{"atproto"}
	if len(c.Collections) > 128 {
		return nil, empty, errors.New("too many OAuth collections")
	}
	for _, collection := range c.Collections {
		if _, err := syntax.ParseNSID(collection); err != nil {
			return nil, empty, fmt.Errorf("invalid OAuth collection: %w", err)
		}
		for _, action := range []string{"create", "update", "delete"} {
			scope := "repo:" + collection + "?action=" + action
			if !slices.Contains(scopes, scope) {
				scopes = append(scopes, scope)
			}
		}
	}
	if len(c.ServiceAuth) > 256 {
		return nil, empty, errors.New("too many OAuth service permissions")
	}
	for _, permission := range c.ServiceAuth {
		if permission.Audience == "" || permission.Method == "*" || permission.Audience == "*" && !dynamicRepositoryMethod(permission.Method) {
			return nil, empty, errors.New("exact service audience and method are required")
		}
		p := auth.Permission{Resource: "rpc", Audience: permission.Audience, Endpoint: []string{permission.Method}}
		scope := p.ScopeString()
		if _, err := auth.ParsePermissionString(scope); err != nil {
			return nil, empty, fmt.Errorf("invalid OAuth service permission: %w", err)
		}
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	config := oauth.NewPublicConfig(c.ClientID, c.CallbackURL, scopes)
	config.UserAgent = "MyContextProtocol/atproto"
	if err := config.SetClientSecret(c.PrivateKey, c.KeyID); err != nil {
		return nil, empty, err
	}
	metadata := config.ClientMetadata()
	jwks := config.PublicJWKS()
	metadata.JWKS = &jwks
	name := "MyContextProtocol"
	metadata.ClientName = &name
	if err := metadata.Validate(c.ClientID); err != nil {
		return nil, empty, err
	}
	client := publicHTTPClient()
	app := oauth.NewClientApp(&config, store)
	app.Client = client
	app.Resolver.Client = client
	// Configure every identity lookup path explicitly, including did:plc. Avoid
	// SDK default directory clients silently bypassing this transport policy.
	app.Dir = &identity.BaseDirectory{PLCURL: "https://plc.directory", HTTPClient: *client, PLCClient: client, UserAgent: config.UserAgent}
	return app, metadata, nil
}

func dynamicRepositoryMethod(method string) bool {
	switch method {
	case "org.tangled.repo.listRepos", "org.tangled.temp.repo.getRepoByName", "org.tangled.temp.git.getArchive", "org.tangled.temp.git.getBranch", "org.tangled.temp.git.listCommits", "org.tangled.temp.git.getTree", "org.tangled.temp.git.getEntry", "org.tangled.temp.git.getBlob", "org.tangled.git.receivePack", "org.tangled.review.submitPatch", "com.atproto.repo.createRecord":
		return true
	default:
		return false
	}
}

// publicURL validates before transport selection, and the dialer independently
// checks the actual resolved socket address to prevent DNS rebinding attacks.
func publicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid public HTTPS URL")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return nil, errors.New("expected HTTPS URL without credentials, fragment, or explicit port")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ssrf.IsPublicIPAddress(ip) {
			return nil, errors.New("non-public IP address")
		}
	} else {
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
			return nil, errors.New("non-public hostname")
		}
		if _, err := syntax.ParseHandle(host); err != nil {
			return nil, errors.New("invalid public hostname")
		}
	}
	return u, nil
}

type publicTransport struct{ inner http.RoundTripper }

func (t publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if _, err := publicURL(r.URL.String()); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(r)
}
func publicHTTPClient() *http.Client {
	transport := ssrf.PublicOnlyTransport()
	// A proxy could resolve and fetch a private destination outside the dialer's
	// socket policy, so OAuth discovery never inherits environment proxy settings.
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: publicTransport{inner: transport}, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("OAuth discovery redirects are disabled")
	}}
}
