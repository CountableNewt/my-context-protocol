package identity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
)

func (s *Service) startGitHubLink(w http.ResponseWriter, r *http.Request) {
	if s.config.ATProto == nil {
		http.NotFound(w, r)
		return
	}
	if !s.allowedATProtoOrigin(r) {
		s.fail(w, 403, "Invalid origin")
		return
	}
	account, err := s.account(r)
	if err != nil {
		s.fail(w, 401, "Not authenticated")
		return
	}
	var input struct {
		ReturnTo string `json:"return_to"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		s.fail(w, 400, "Invalid link request")
		return
	}
	if input.ReturnTo == "" {
		input.ReturnTo = s.config.FrontendOrigins[0] + "/account"
	}
	target, err := validateReturnTo(input.ReturnTo, s.config.FrontendOrigins)
	if err != nil {
		s.fail(w, 400, "Invalid return_to")
		return
	}
	state, err := signStateOfKind(s.key, target, s.now(), "gh_link_v1")
	if err != nil {
		s.fail(w, 503, "Could not start account link")
		return
	}
	browser, err := security.NewOpaqueToken()
	if err != nil {
		s.fail(w, 503, "Could not start account link")
		return
	}
	if err = s.config.ATProto.Accounts.SaveLoginFlow(r.Context(), state, browser, ATProtoLoginFlow{target, account.ID}, s.now().Add(10*time.Minute)); err != nil {
		s.fail(w, 503, "Could not persist account link")
		return
	}
	s.atprotoCookie(w, browser, false)
	query := url.Values{"client_id": {s.config.ClientID}, "redirect_uri": {s.config.RedirectURI}, "state": {state}, "scope": {"read:user user:email repo read:org"}}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"authorization_url": "https://github.com/login/oauth/authorize?" + query.Encode()})
}
func (s *Service) callbackGitHubLink(w http.ResponseWriter, r *http.Request, target string) {
	if s.config.ATProto == nil {
		s.fail(w, 400, "Account linking is not enabled")
		return
	}
	cookie, err := r.Cookie("atproto-login")
	if err != nil || len(cookie.Value) != 64 {
		s.fail(w, 400, "Invalid browser authorization state")
		return
	}
	flow, err := s.config.ATProto.Accounts.ConsumeLoginFlow(r.Context(), r.URL.Query().Get("state"), cookie.Value, s.now())
	s.atprotoCookie(w, "", true)
	if err != nil || flow.LinkAccountID == "" || flow.ReturnTo != target {
		s.fail(w, 400, "Invalid or expired account link")
		return
	}
	if _, err = validateReturnTo(target, s.config.FrontendOrigins); err != nil {
		s.fail(w, 400, "Invalid return_to")
		return
	}
	account, err := s.account(r)
	if err != nil || account.ID != flow.LinkAccountID {
		s.fail(w, 403, "The account linking session has changed")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.fail(w, 400, "GitHub authorization failed")
		return
	}
	token, err := s.github.Exchange(r.Context(), s.config.ClientID, s.config.ClientSecret, code, s.config.RedirectURI)
	if err != nil {
		s.fail(w, 400, "GitHub authorization failed")
		return
	}
	user, err := s.github.User(r.Context(), token)
	if err != nil {
		s.fail(w, 400, "GitHub identity verification failed")
		return
	}
	encrypted, err := s.cipher.Encrypt(token)
	if err != nil {
		s.fail(w, 503, "Could not secure credentials")
		return
	}
	if _, err = s.config.ATProto.Accounts.LinkGitHubAccount(r.Context(), account.ID, user, encrypted); err != nil {
		s.fail(w, 409, "GitHub identity is already linked or cannot replace this account identity")
		return
	}
	if err = s.establishSession(w, r, account.ID); err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}
