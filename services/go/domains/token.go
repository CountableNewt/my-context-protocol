package domains

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// accessToken serializes minting so concurrent domain calls share one token.
// Never replay a domain mutation after an authentication failure.
func (s *Service) accessToken(ctx context.Context) (string, error) {
	if s.config.Token != "" {
		return s.config.Token, nil
	}
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", &Error{503, "Vercel integration authentication unavailable"}
	}
	if s.cachedToken != "" && s.now().Before(s.tokenExpiry) {
		return s.cachedToken, nil
	}
	fail := func() (string, error) { return "", &Error{503, "Vercel integration authentication unavailable"} }
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"client_credentials"}, "installation_id": {s.config.IntegrationInstallationID}}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.api+"/v2/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return fail()
	}
	req.SetBasicAuth(s.config.IntegrationClientID, s.config.IntegrationClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	started := s.now()
	res, err := s.http.Do(req)
	if err != nil {
		return fail()
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fail()
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return fail()
	}
	var response struct {
		Token        string `json:"access_token"`
		Type         string `json:"token_type"`
		Expires      int64  `json:"expires_in"`
		Installation string `json:"installation_id"`
		Team         string `json:"team_id"`
	}
	if json.Unmarshal(raw, &response) != nil || !validBearer(response.Token) || !strings.EqualFold(response.Type, "Bearer") || response.Expires <= 60 || response.Expires > 3600 || response.Installation != s.config.IntegrationInstallationID || response.Team != s.config.TeamID {
		return fail()
	}
	expiry := started.Add(time.Duration(response.Expires)*time.Second - time.Minute)
	if !s.now().Before(expiry) {
		return fail()
	}
	s.cachedToken, s.tokenExpiry = response.Token, expiry
	return s.cachedToken, nil
}

func validBearer(token string) bool {
	if token == "" {
		return false
	}
	for _, c := range token {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
