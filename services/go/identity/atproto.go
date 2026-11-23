package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

type ATProtoOAuth interface {
	StartAuth(context.Context, string) (string, string, error)
	ProcessCallback(context.Context, url.Values) (*oauth.ClientSessionData, error)
	Logout(context.Context, syntax.DID, string) error
}
type ATProtoConfig struct {
	Client   ATProtoOAuth
	Accounts ATProtoAccounts
	Metadata oauth.ClientMetadata
	Handle   func(context.Context, syntax.DID) (string, error)
}

func (s *Service) serveATProto(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/auth/atproto/status" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": s.config.ATProto != nil})
		return
	}
	config := s.config.ATProto
	if config == nil || config.Client == nil || config.Accounts == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/auth/atproto/client-metadata.json":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config.Metadata)
	case "/auth/atproto", "/auth/cli/atproto":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.startATProto(w, r, r.URL.Query().Get("handle"), r.URL.Query().Get("return_to"), "", r.URL.Path == "/auth/cli/atproto", false)
	case "/auth/atproto/link":
		if r.Method != "POST" {
			s.method(w)
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
			Handle   string `json:"handle"`
			ReturnTo string `json:"return_to"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			s.fail(w, 400, "Invalid link request")
			return
		}
		s.startATProto(w, r, input.Handle, input.ReturnTo, account.ID, false, true)
	case "/auth/atproto/callback":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.callbackATProto(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Service) allowedATProtoOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if u, err := url.Parse(r.Header.Get("Referer")); err == nil {
			origin = u.Scheme + "://" + u.Host
		}
	}
	for _, allowed := range s.config.FrontendOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}
func (s *Service) atprotoCookie(w http.ResponseWriter, value string, clear bool) {
	u, _ := url.Parse(s.config.FrontendOrigins[0])
	c := &http.Cookie{Name: "atproto-login", Value: value, Path: "/", HttpOnly: true, Secure: u.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600}
	if clear {
		c.MaxAge = -1
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}
func (s *Service) startATProto(w http.ResponseWriter, r *http.Request, handle, target, link string, cli, jsonResponse bool) {
	if _, err := syntax.ParseAtIdentifier(handle); err != nil {
		s.fail(w, 400, "A valid ATProto handle or DID is required")
		return
	}
	var err error
	if cli {
		target, err = ValidateCLIReturnTo(target)
	} else {
		if target == "" {
			target = s.config.FrontendOrigins[0] + "/account"
		}
		target, err = validateReturnTo(target, s.config.FrontendOrigins)
	}
	if err != nil {
		s.fail(w, 400, "Invalid return_to")
		return
	}
	browser, err := security.NewOpaqueToken()
	if err != nil {
		s.fail(w, 503, "Could not start authorization")
		return
	}
	redirect, state, err := s.config.ATProto.Client.StartAuth(r.Context(), handle)
	if err != nil {
		s.fail(w, 503, "ATProto authorization is temporarily unavailable")
		return
	}
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || state == "" {
		s.fail(w, 503, "Invalid ATProto authorization response")
		return
	}
	if err = s.config.ATProto.Accounts.SaveLoginFlow(r.Context(), state, browser, ATProtoLoginFlow{target, link}, s.now().Add(10*time.Minute)); err != nil {
		s.fail(w, 503, "Could not persist authorization")
		return
	}
	s.atprotoCookie(w, browser, false)
	if jsonResponse {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"authorization_url": redirect})
		return
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}
func (s *Service) callbackATProto(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("atproto-login")
	if err != nil || len(cookie.Value) != 64 {
		s.fail(w, 400, "Invalid browser authorization state")
		return
	}
	flow, err := s.config.ATProto.Accounts.ConsumeLoginFlow(r.Context(), r.URL.Query().Get("state"), cookie.Value, s.now())
	s.atprotoCookie(w, "", true)
	if err != nil {
		s.fail(w, 400, "Invalid or expired authorization state")
		return
	}
	if _, err = validateReturnTo(flow.ReturnTo, s.config.FrontendOrigins); err != nil {
		if _, err = ValidateCLIReturnTo(flow.ReturnTo); err != nil {
			s.fail(w, 400, "Invalid return_to")
			return
		}
	}
	if flow.LinkAccountID != "" {
		account, e := s.account(r)
		if e != nil || account.ID != flow.LinkAccountID {
			s.fail(w, 403, "The account linking session has changed")
			return
		}
	}
	data, err := s.config.ATProto.Client.ProcessCallback(r.Context(), r.URL.Query())
	if err != nil || data == nil {
		s.fail(w, 400, "ATProto authorization failed")
		return
	}
	ident := ATProtoIdentity{DID: data.AccountDID.String(), PDSURL: data.HostURL, SessionID: data.SessionID}
	if s.config.ATProto.Handle != nil {
		ident.Handle, err = s.config.ATProto.Handle(r.Context(), data.AccountDID)
		if err != nil {
			s.fail(w, 503, "Could not verify ATProto identity")
			return
		}
	}
	account, err := s.config.ATProto.Accounts.UpsertATProtoAccount(r.Context(), ident, flow.LinkAccountID)
	if err != nil || account == nil {
		_ = s.config.ATProto.Client.Logout(r.Context(), data.AccountDID, data.SessionID)
		if errors.Is(err, ErrIdentityConflict) {
			s.fail(w, 409, "ATProto identity is already linked")
		} else {
			s.fail(w, 503, "Auth service temporarily unavailable")
		}
		return
	}
	if err = s.establishSession(w, r, account.ID); err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	if flow.LinkAccountID != "" {
		http.Redirect(w, r, flow.ReturnTo, http.StatusFound)
		return
	}
	handoff, err := security.NewOpaqueToken()
	if err != nil {
		s.fail(w, 503, "Could not complete authorization")
		return
	}
	if err = s.store.IssueHandoff(r.Context(), handoff, account.ID, s.now().Add(5*time.Minute)); err != nil {
		s.fail(w, 503, "Could not complete authorization")
		return
	}
	http.Redirect(w, r, addQuery(flow.ReturnTo, "auth_token", handoff), http.StatusFound)
}
