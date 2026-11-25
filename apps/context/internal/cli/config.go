package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type CLIError struct {
	Kind, Message string
	Status        int
	Payload       any
}

func (e *CLIError) Error() string {
	if e.Kind == "api" {
		return fmt.Sprintf("API request failed (HTTP %d)", e.Status)
	}
	return e.Message
}
func (e *CLIError) ExitCode() int {
	switch {
	case e.Kind == "usage":
		return 2
	case e.Kind == "canceled":
		return 130
	case e.Status == 401:
		return 3
	case e.Status == 403:
		return 4
	default:
		return 1
	}
}
func Usage(message string) error        { return &CLIError{Kind: "usage", Message: message} }
func RequestError(message string) error { return &CLIError{Kind: "request", Message: message} }

type Profile struct {
	URL        string           `json:"url"`
	WebURL     string           `json:"web_url"`
	Session    string           `json:"session"`
	Project    string           `json:"project"`
	Token      string           `json:"token,omitempty"`
	OAuth      *OAuthCredential `json:"oauth,omitempty"`
	ServiceDID string           `json:"service_did,omitempty"`
}
type Configuration struct {
	Profiles map[string]Profile `json:"profiles"`
}

func LoadConfiguration(path string) (Configuration, error) {
	c := Configuration{Profiles: map[string]Profile{}}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, e
}
func (c Configuration) Save(path string) error {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".myctx-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func Environment(key string, env map[string]string) string {
	return First(env["MYCTX_"+key], env["CONTEXT_"+key])
}
func First(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func ConfigurationPath(env map[string]string) string {
	if p := Environment("CONFIG", env); p != "" {
		return p
	}
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	base := First(env["XDG_CONFIG_HOME"], filepath.Join(home, ".config"))
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support")
	}
	primary := filepath.Join(base, "myctx", "config.json")
	legacy := filepath.Join(base, "context", "config.json")
	if _, e := os.Stat(primary); os.IsNotExist(e) {
		if _, e = os.Stat(legacy); e == nil {
			return legacy
		}
	}
	return primary
}
func ValidateOrigin(raw string) (string, error) {
	bad := func() (string, error) {
		return "", Usage("URL must be an HTTPS API origin without credentials, path, query or fragment (HTTP is allowed for loopback)")
	}
	for _, r := range raw {
		if r <= 32 || r == 127 {
			return bad()
		}
	}
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return bad()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1")) {
		return bad()
	}
	return strings.TrimSuffix(raw, "/"), nil
}
func ValidateAPIBaseURL(raw string) (string, error) {
	if strings.Contains(raw, "#") {
		return "", Usage("API URL must not contain a fragment")
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "", Usage("Invalid API URL")
	}
	p := u.EscapedPath()
	if p != "" && p != "/" && p != "/api" && p != "/api/" {
		return "", Usage("API URL must be an HTTPS origin, optionally ending in /api")
	}
	u.Path = ""
	u.RawPath = ""
	origin, e := ValidateOrigin(u.String())
	if e != nil {
		return "", e
	}
	if p == "/api" || p == "/api/" {
		origin += "/api"
	}
	return origin, nil
}
