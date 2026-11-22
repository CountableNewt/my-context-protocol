package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type GitHub interface {
	Exchange(context.Context, string, string, string, string) (string, error)
	User(context.Context, string) (GitHubUser, error)
}
type GitHubHTTP struct{ client *http.Client }

func NewGitHubHTTP() *GitHubHTTP {
	return &GitHubHTTP{client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (g *GitHubHTTP) request(ctx context.Context, method, path, token string, body any, target any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MyContextProtocol-OAuth")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return errors.New("GitHub request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("GitHub request rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("invalid GitHub response")
	}
	if json.Unmarshal(raw, target) != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}
func (g *GitHubHTTP) Exchange(ctx context.Context, client, secret, code, redirect string) (string, error) {
	var response struct {
		AccessToken string `json:"access_token"`
	}
	err := g.request(ctx, "POST", "https://github.com/login/oauth/access_token", "", map[string]string{"client_id": client, "client_secret": secret, "code": code, "redirect_uri": redirect}, &response)
	if err != nil {
		return "", err
	}
	if response.AccessToken == "" {
		return "", errors.New("token exchange failed")
	}
	return response.AccessToken, nil
}
func (g *GitHubHTTP) User(ctx context.Context, token string) (GitHubUser, error) {
	var user GitHubUser
	err := g.request(ctx, "GET", "https://api.github.com/user", token, nil, &user)
	if err != nil {
		return user, err
	}
	if user.ID <= 0 {
		return user, errors.New("invalid GitHub user")
	}
	return user, nil
}
