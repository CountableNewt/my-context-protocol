package xrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Principal contains verified identity and normalized method permissions only.
// An implementation must not populate these fields from unverified JWT claims.
type Principal struct {
	AccountID string
	DID       string
	Scopes    map[string]bool
	IsAdmin   bool
}

type Verifier interface {
	Verify(context.Context, *http.Request) (Principal, error)
}

type Input struct {
	Parameters map[string]string
	Body       map[string]json.RawMessage
}

type Result struct {
	Value any
}

// Invoker must perform current account, tenant and resource ownership checks,
// business validation, rate limiting and canonical publication before success.
// Transport permission is necessary but does not establish resource ownership.
type Invoker interface {
	Invoke(context.Context, Principal, Operation, Input) (Result, error)
}

type Config struct {
	Verifier      Verifier
	Invoker       Invoker
	MaxInputBytes int64
}

type Handler struct{ config Config }

// Error represents a reviewed public application failure. Other errors are
// replaced with a generic service failure to avoid leaking infrastructure data.
type Error struct {
	Status            int
	Code              string
	Message           string
	RetryAfterSeconds int
}

func (e *Error) Error() string { return e.Message }

func New(config Config) (*Handler, error) {
	if config.Verifier == nil || config.Invoker == nil {
		return nil, errors.New("XRPC verifier and invoker are required")
	}
	if config.MaxInputBytes == 0 {
		config.MaxInputBytes = 64 << 10
	}
	if config.MaxInputBytes < 1 || config.MaxInputBytes > 2<<20 {
		return nil, errors.New("XRPC input limit must be between 1 byte and 2 MiB")
	}
	return &Handler{config: config}, nil
}

func failure(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	op, ok := lookup(r.URL.Path)
	if !ok || r.URL.EscapedPath() != r.URL.Path {
		failure(w, 404, "NotFound", "Unknown XRPC method")
		return
	}
	if r.Method != op.Method {
		w.Header().Set("Allow", op.Method)
		failure(w, 405, "InvalidRequest", "HTTP method does not match this XRPC method")
		return
	}
	// Never accept a browser session alone. The verifier validates Bearer/DPoP
	// credentials, their proof, expiry, audience and normalized exact scope.
	authorization := strings.Fields(r.Header.Get("Authorization"))
	if len(authorization) != 2 || (authorization[0] != "Bearer" && authorization[0] != "DPoP") {
		failure(w, 401, "AuthenticationRequired", "Verified API authorization is required")
		return
	}
	principal, err := h.config.Verifier.Verify(r.Context(), r)
	if err != nil || principal.AccountID == "" || !strings.HasPrefix(principal.DID, "did:") {
		failure(w, 401, "AuthenticationRequired", "Invalid API authorization")
		return
	}
	if !principal.Scopes[op.Scope()] || op.Admin && !principal.IsAdmin {
		failure(w, 403, "Forbidden", "Permission denied")
		return
	}
	input, err := h.input(w, r, op)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			failure(w, 413, "InvalidRequest", "Input is too large")
		} else {
			failure(w, 400, "InvalidRequest", err.Error())
		}
		return
	}
	result, err := h.config.Invoker.Invoke(r.Context(), principal, op, input)
	if err != nil {
		var public *Error
		if errors.As(err, &public) && allowedError(public) {
			if public.Status == 429 && public.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(public.RetryAfterSeconds))
			}
			failure(w, public.Status, public.Code, public.Message)
		} else {
			failure(w, 503, "ServiceUnavailable", "Service temporarily unavailable")
		}
		return
	}
	if op.NoOutput {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if result.Value == nil {
		failure(w, 503, "ServiceUnavailable", "Service returned no result")
		return
	}
	raw, err := json.Marshal(result.Value)
	if err != nil {
		failure(w, 503, "ServiceUnavailable", "Service returned an invalid result")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(raw, '\n'))
}

func allowedError(e *Error) bool {
	return map[string]int{"InvalidRequest": 400, "AuthenticationRequired": 401, "Forbidden": 403, "NotFound": 404, "Conflict": 409, "RateLimitExceeded": 429, "ServiceUnavailable": 503}[e.Code] == e.Status && e.Status != 0
}

func (h *Handler) input(w http.ResponseWriter, r *http.Request, op Operation) (Input, error) {
	input := Input{Parameters: map[string]string{}, Body: map[string]json.RawMessage{}}
	if len(r.URL.RawQuery) > 8192 {
		return input, &http.MaxBytesError{Limit: 8192}
	}
	if op.Method == "GET" {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			return input, errors.New("Query must not contain a body")
		}
		query, err := parseQuery(r)
		if err != nil {
			return input, err
		}
		input.Parameters = query
		for name, value := range query {
			field, ok := findField(op, name)
			if !ok {
				return input, errors.New("Unknown query parameter")
			}
			if err := validateString(field, value); err != nil {
				return input, err
			}
		}
	} else {
		if r.URL.RawQuery != "" {
			return input, errors.New("Procedure arguments belong in the JSON body")
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			return input, errors.New("Procedure requires application/json")
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.config.MaxInputBytes))
		token, err := decoder.Token()
		if err != nil {
			return input, invalidJSON(err)
		}
		if token != json.Delim('{') {
			return input, errors.New("Procedure input must be a JSON object")
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return input, invalidJSON(err)
			}
			name, ok := key.(string)
			if !ok {
				return input, errors.New("Invalid procedure property")
			}
			field, ok := findField(op, name)
			if !ok {
				return input, errors.New("Unknown procedure property")
			}
			if _, exists := input.Body[name]; exists {
				return input, errors.New("Duplicate procedure property")
			}
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return input, invalidJSON(err)
			}
			if field.Boolean {
				if string(value) != "true" && string(value) != "false" {
					return input, errors.New("Expected boolean property")
				}
			} else {
				var text string
				if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &text) != nil {
					return input, errors.New("Expected string property")
				}
				if err := validateString(field, text); err != nil {
					return input, err
				}
			}
			input.Body[name] = value
		}
		if _, err := decoder.Token(); err != nil {
			return input, invalidJSON(err)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			if err != nil {
				return input, invalidJSON(err)
			}
			return input, errors.New("Multiple JSON values are not allowed")
		}
	}
	for _, field := range op.Fields {
		_, queryExists := input.Parameters[field.Name]
		_, bodyExists := input.Body[field.Name]
		if field.Required && !queryExists && !bodyExists {
			return input, errors.New("Missing required argument: " + field.Name)
		}
	}
	return input, nil
}

func invalidJSON(err error) error {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		return large
	}
	return errors.New("Malformed JSON input")
}

func findField(op Operation, name string) (Field, bool) {
	for _, field := range op.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return Field{}, false
}

func validateString(field Field, value string) error {
	if !utf8.ValidString(value) {
		return errors.New("Invalid UTF-8 argument")
	}
	length := utf8.RuneCountInString(value)
	if length < field.MinLength || field.MaxLength > 0 && length > field.MaxLength {
		return errors.New("Argument length is outside its contract")
	}
	return nil
}
