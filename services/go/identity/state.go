package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type statePayload struct {
	Kind     string `json:"k"`
	ReturnTo string `json:"rt"`
	Expires  int64  `json:"exp"`
	Nonce    string `json:"n"`
}

func signState(key []byte, returnTo string, now time.Time) (string, error) {
	return signStateOfKind(key, returnTo, now, "gh_oauth_v1")
}
func signStateOfKind(key []byte, returnTo string, now time.Time, kind string) (string, error) {
	nonce, err := UUID()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(statePayload{Kind: kind, ReturnTo: returnTo, Expires: now.Unix() + 600, Nonce: nonce})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func verifyState(key []byte, state string, now time.Time) (string, error) {
	return verifyStateOfKind(key, state, now, "gh_oauth_v1")
}
func verifyStateOfKind(key []byte, state string, now time.Time, kind string) (string, error) {
	if len(state) > 8192 {
		return "", errors.New("invalid state")
	}
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid state")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("invalid state")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid state")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errors.New("invalid state")
	}
	var payload statePayload
	if json.Unmarshal(body, &payload) != nil || payload.Kind != kind || payload.Expires < now.Unix() || payload.Nonce == "" {
		return "", errors.New("invalid state")
	}
	return payload.ReturnTo, nil
}
func ValidateCLIReturnTo(raw string) (string, error) {
	invalid := errors.New("CLI return_to must be http://127.0.0.1:PORT/callback with a single random state parameter")
	if len(raw) > 512 {
		return "", invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Fragment != "" || u.EscapedPath() != "/callback" {
		return "", invalid
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["state"]) != 1 {
		return "", invalid
	}
	nonce := q.Get("state")
	if len(nonce) < 43 || len(nonce) > 128 {
		return "", invalid
	}
	for _, c := range nonce {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", invalid
		}
	}
	if raw != "http://127.0.0.1:"+strconv.Itoa(port)+"/callback?state="+nonce {
		return "", invalid
	}
	return raw, nil
}
func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("invalid frontend origin")
	}
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}
func validateReturnTo(raw string, origins []string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return "", errors.New("invalid return_to")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("invalid return_to")
	}
	for _, origin := range origins {
		if strings.EqualFold(u.Scheme+"://"+u.Host, origin) {
			return raw, nil
		}
	}
	return "", errors.New("return_to origin is not allowed")
}
func validateRelativePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4096 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\x00") {
		return "", errors.New("invalid redirect")
	}
	return raw, nil
}
