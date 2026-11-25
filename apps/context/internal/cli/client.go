package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	OAuthHTTPClient                   *http.Client
	Origin, Session, WebOrigin, Token string
	Timeout                           time.Duration
	OAuth                             *OAuthCredential
	PersistOAuth                      func(*OAuthCredential) error
}

func (c Client) RequestURL(path string, query url.Values) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, Usage("API path must be an origin-relative path") }
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") {
		return bad()
	}
	for _, r := range path {
		if r < 32 || r == 127 {
			return bad()
		}
	}
	u, e := url.Parse(path)
	if e != nil || u.Host != "" || u.Scheme != "" || strings.Contains(path, "#") {
		return bad()
	}
	for _, s := range strings.Split(u.EscapedPath(), "/") {
		d, e := url.PathUnescape(s)
		if e != nil || d == "." || d == ".." || strings.ContainsAny(d, "/\\") {
			return bad()
		}
		for _, r := range d {
			if r < 32 || r == 127 {
				return bad()
			}
		}
	}
	q := u.Query()
	for k, vs := range query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	u.ForceQuery = false
	return url.Parse(c.Origin + u.String())
}
func (c Client) Request(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, *http.Response, error) {
	u, e := c.RequestURL(path, query)
	if e != nil {
		return nil, nil, e
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if e != nil {
		return nil, nil, e
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MyContextCLI")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" && c.WebOrigin != "" {
		req.Header.Set("Origin", c.WebOrigin)
	}
	if c.Session != "" && c.OAuth == nil && c.Token == "" && !strings.HasPrefix(path, "/xrpc/") {
		if strings.ContainsAny(c.Session, "\r\n;\x00") {
			return nil, nil, Usage("Invalid session cookie")
		}
		req.Header.Set("Cookie", "vapor-session="+c.Session)
	}
	token := ""
	if strings.HasPrefix(path, "/xrpc/") {
		token = c.Token
		if token == "" {
			token, e = c.serviceToken(ctx, strings.TrimPrefix(strings.SplitN(path, "?", 2)[0], "/xrpc/"))
			if e != nil {
				return nil, nil, e
			}
		}
		req.Header.Del("Cookie")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := httpClient.Do(req)
	if e != nil {
		return nil, nil, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if len(b) > 32<<20 {
		return nil, res, RequestError("Response exceeds 32 MiB")
	}
	return b, res, e
}
func (c Client) Checked(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, *http.Response, error) {
	b, r, e := c.Request(ctx, method, path, query, body)
	if e != nil {
		return b, r, e
	}
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return b, r, nil
	}
	var payload any
	if r.StatusCode >= 300 && r.StatusCode < 400 {
		payload = map[string]any{"location": r.Header.Get("Location")}
	} else {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if d.Decode(&payload) != nil {
			payload = map[string]any{"message": http.StatusText(r.StatusCode)}
		}
	}
	return b, r, &CLIError{Kind: "api", Status: r.StatusCode, Payload: payload}
}
