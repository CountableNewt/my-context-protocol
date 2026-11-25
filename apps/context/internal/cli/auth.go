package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// OAuthCredential retains refreshable DPoP credentials and the exact API target
// approved at login. It is stored only in the private configuration file.
type OAuthCredential struct {
	CallbackURL string                  `json:"callback_url"`
	Origin      string                  `json:"origin"`
	ServiceDID  string                  `json:"service_did"`
	Data        oauth.ClientSessionData `json:"data"`
}

func ValidateServiceDID(value string) error {
	parts := strings.Split(value, "#")
	if len(parts) != 2 || parts[1] == "" {
		return Usage("--service-did must identify an exact DID service (did:...#service)")
	}
	if _, e := syntax.ParseDID(parts[0]); e != nil {
		return Usage("--service-did must identify an exact DID service (did:...#service)")
	}
	if strings.ContainsAny(parts[1], " /?\r\n\t") {
		return Usage("Invalid service DID fragment")
	}
	return nil
}
func oauthConfig(callback, audience string) oauth.ClientConfig {
	scopes := []string{"atproto"}
	for _, method := range OAuthMethods() {
		p := auth.Permission{Resource: "rpc", Audience: audience, Endpoint: []string{method}}
		scopes = append(scopes, p.ScopeString())
	}
	config := oauth.NewLocalhostConfig(callback, scopes)
	config.UserAgent = "MyContextCLI"
	return config
}

func ATProtoLogin(ctx context.Context, client *Client, handle, serviceDID string, noBrowser bool, diagnostics func(string)) (*OAuthCredential, error) {
	if handle == "" {
		return nil, Usage("ATProto login requires --handle")
	}
	if _, e := syntax.ParseAtIdentifier(handle); e != nil {
		return nil, Usage("--handle must be an ATProto handle or DID")
	}
	if e := ValidateServiceDID(serviceDID); e != nil {
		return nil, e
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	defer listener.Close()
	callback := "http://" + listener.Addr().String() + "/callback"
	config := oauthConfig(callback, serviceDID)
	store := oauth.NewMemStore()
	app := oauth.NewClientApp(&config, store)
	configureOAuthHTTP(app, client.OAuthHTTPClient)
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	authorize, e := app.StartAuthFlow(loginCtx, handle)
	if e != nil {
		return nil, e
	}
	type result struct {
		data *oauth.ClientSessionData
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(q["state"]) != 1 || len(q["iss"]) != 1 || len(q["code"])+len(q["error"]) != 1 {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		// Reject unsolicited callbacks without consuming the legitimate request.
		if _, e := store.GetAuthRequestInfo(loginCtx, q.Get("state")); e != nil {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		data, e := app.ProcessCallback(loginCtx, q)
		if e != nil {
			http.Error(w, "Authorization failed. Return to MyContextCLI to retry.", 400)
		} else {
			fmt.Fprint(w, "Authorization received. You can return to MyContextCLI.")
		}
		select {
		case done <- result{data, e}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	defer server.Close()
	go server.Serve(listener)
	diagnostics("Open this URL to authenticate with ATProto:\n" + authorize + "\n")
	if !noBrowser {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "open"
		}
		cmd := exec.Command(command, authorize)
		if e := cmd.Start(); e != nil {
			diagnostics("Browser could not open automatically; open the URL above.\n")
		} else {
			go cmd.Wait()
		}
	}
	select {
	case <-loginCtx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, RequestError("Login timed out after 5 minutes")
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		credential := &OAuthCredential{CallbackURL: callback, Origin: client.Origin, ServiceDID: serviceDID, Data: *r.data}
		client.OAuth = credential
		return credential, nil
	}
}

func (c Client) serviceToken(ctx context.Context, method string) (string, error) {
	if c.OAuth == nil {
		return "", RequestError("ATProto authorization required; run auth login --handle <handle> --service-did <did#service>")
	}
	credential := c.OAuth
	if credential.Origin != c.Origin {
		return "", Usage("OAuth credentials belong to a different API URL; authenticate this origin explicitly")
	}
	if e := ValidateServiceDID(credential.ServiceDID); e != nil {
		return "", e
	}
	config := oauthConfig(credential.CallbackURL, credential.ServiceDID)
	store := oauth.NewMemStore()
	if e := store.SaveSession(ctx, credential.Data); e != nil {
		return "", e
	}
	app := oauth.NewClientApp(&config, store)
	configureOAuthHTTP(app, c.OAuthHTTPClient)
	session, e := app.ResumeSession(ctx, credential.Data.AccountDID, credential.Data.SessionID)
	if e != nil {
		return "", e
	}
	var persistErr error
	session.PersistSessionCallback = func(_ context.Context, data *oauth.ClientSessionData) {
		credential.Data = *data
		if c.PersistOAuth != nil {
			persistErr = c.PersistOAuth(credential)
		}
	}
	var response struct {
		Token string `json:"token"`
	}
	e = session.APIClient().Get(ctx, syntax.NSID("com.atproto.server.getServiceAuth"), map[string]any{"aud": credential.ServiceDID, "lxm": method, "exp": time.Now().Add(60 * time.Second).Unix()}, &response)
	if persistErr != nil {
		return "", fmt.Errorf("persist OAuth refresh: %w", persistErr)
	}
	if e != nil {
		return "", e
	}
	if response.Token == "" {
		return "", RequestError("PDS did not return a service authorization token")
	}
	return response.Token, nil
}

func AuthorizationURL(ctx context.Context, client Client, callback string) (string, error) {
	client.Session = ""
	client.Token = ""
	client.OAuth = nil
	_, response, e := client.Request(ctx, "GET", "/auth/cli/github", url.Values{"return_to": {callback}}, nil)
	if e != nil {
		return "", e
	}
	if response.StatusCode == 404 {
		return "", RequestError("Server does not support CLI login; deploy the accompanying Gateway version or verify the API origin")
	}
	location := response.Header.Get("Location")
	u, e := url.Parse(location)
	valid := e == nil && (response.StatusCode == 302 || response.StatusCode == 303) && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.EscapedPath() == "/login/oauth/authorize" && u.Fragment == ""
	if valid {
		q, e := url.ParseQuery(u.RawQuery)
		valid = e == nil && len(q["state"]) == 1 && q.Get("state") != "" && len(q["client_id"]) == 1 && q.Get("client_id") != ""
	}
	if !valid {
		return "", RequestError("CLI login returned an invalid GitHub authorization redirect")
	}
	return location, nil
}

func BrowserLogin(ctx context.Context, client *Client, noBrowser bool, provider, handle string, diagnostics func(string)) (string, error) {
	client.OAuth = nil
	client.Token = ""
	client.Session = ""
	if provider != "github" {
		return "", Usage("Use native ATProto sign-in for --provider atproto")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return "", e
	}
	defer listener.Close()
	random := make([]byte, 32)
	if _, e := rand.Read(random); e != nil {
		return "", e
	}
	nonce := base64.RawURLEncoding.EncodeToString(random)
	callback := "http://" + listener.Addr().String() + "/callback?state=" + nonce
	authorize, e := AuthorizationURL(ctx, *client, callback)
	if e != nil {
		return "", e
	}
	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || r.Method != "GET" || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(nonce)) != 1 {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		if len(q["error"]) == 1 && q.Get("error") != "" {
			http.Error(w, "Authorization failed. Return to MyContextCLI to retry.", 400)
			select {
			case done <- result{err: RequestError("Authorization was declined or failed; run auth login to try again")}:
			default:
			}
			return
		}
		if len(q["auth_token"]) != 1 || q.Get("auth_token") == "" {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		fmt.Fprint(w, "Authorization received. You can return to MyContextCLI.")
		select {
		case done <- result{token: q.Get("auth_token")}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	defer server.Close()
	go server.Serve(listener)
	diagnostics("Open this URL to authenticate with GitHub:\n" + authorize + "\n")
	if !noBrowser {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "open"
		}
		cmd := exec.Command(command, authorize)
		if e := cmd.Start(); e != nil {
			diagnostics("Browser could not open automatically; open the URL above.\n")
		} else {
			go cmd.Wait()
		}
	}
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	var token string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", RequestError("Login timed out after 5 minutes")
	case result := <-done:
		if result.err != nil {
			return "", result.err
		}
		token = result.token
	}
	_, response, e := client.Checked(ctx, "GET", "/auth/confirm", url.Values{"token": {token}}, nil)
	if e != nil {
		return "", e
	}
	session := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == "vapor-session" && cookie.Value != "" {
			session = cookie.Value
		}
	}
	if session == "" {
		return "", RequestError("Authorization did not return a session cookie")
	}
	if client.WebOrigin == "" {
		if origin, e := ValidateOrigin(response.Header.Get("X-Context-Frontend-Origin")); e == nil {
			client.WebOrigin = origin
		}
	}
	client.Session = session
	_, _, e = client.Checked(ctx, "GET", "/auth/me", nil, nil)
	return session, e
}

// Preserve the SDK public-network transport while forbidding redirect credential forwarding.
func configureOAuthHTTP(app *oauth.ClientApp, override *http.Client) {
	client := *app.Client
	if override != nil {
		client = *override
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	app.Client = &client
	app.Resolver.Client = &client
}

// RevokeOAuth invalidates the refreshable PDS session before local deletion.
func (c Client) RevokeOAuth(ctx context.Context) error {
	if c.OAuth == nil {
		return nil
	}
	credential := c.OAuth
	if credential.Origin != c.Origin {
		return Usage("OAuth credentials belong to a different API URL")
	}
	config := oauthConfig(credential.CallbackURL, credential.ServiceDID)
	store := oauth.NewMemStore()
	if e := store.SaveSession(ctx, credential.Data); e != nil {
		return e
	}
	app := oauth.NewClientApp(&config, store)
	configureOAuthHTTP(app, c.OAuthHTTPClient)
	session, e := app.ResumeSession(ctx, credential.Data.AccountDID, credential.Data.SessionID)
	if e != nil {
		return e
	}
	return session.RevokeSession(ctx)
}
