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
	// ProjectID and ClientID are set only by tenant-scoped credential verification.
	ProjectID string
	ClientID  string
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
	TelemetryPublication string
	Value                any
	Status               int
}

// Invoker must perform current account, tenant and resource ownership checks,
// business validation, rate limiting and canonical publication before success.
// Transport permission is necessary but does not establish resource ownership.
type Invoker interface {
	Invoke(context.Context, Principal, Operation, Input) (Result, error)
}

type InvokerFunc func(context.Context, Principal, Operation, Input) (Result, error)

func (f InvokerFunc) Invoke(ctx context.Context, p Principal, o Operation, i Input) (Result, error) {
	return f(ctx, p, o, i)
}

type VerifierFunc func(context.Context, *http.Request) (Principal, error)

func (f VerifierFunc) Verify(ctx context.Context, r *http.Request) (Principal, error) {
	return f(ctx, r)
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
		config.MaxInputBytes = 4 << 20
	}
	if config.MaxInputBytes < 1 || config.MaxInputBytes > 4<<20 {
		return nil, errors.New("XRPC input limit must be between 1 byte and 4 MiB")
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
	// XRPC accepts explicit API credentials and never ambient browser cookies.
	// Public CORS permits third-party clients without credentialed cookie access.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Retry-After, WWW-Authenticate, X-Myctx-Telemetry-Publication")
	op, ok := lookup(r.URL.Path)
	if !ok || r.URL.EscapedPath() != r.URL.Path {
		failure(w, 404, "NotFound", "Unknown XRPC method")
		return
	}
	if r.Method == http.MethodOptions {
		if requested := r.Header.Get("Access-Control-Request-Method"); requested != op.Method {
			failure(w, 405, "InvalidRequest", "Requested method does not match this XRPC method")
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", op.Method)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, DPoP")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
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
	if result.TelemetryPublication == "pending" || result.TelemetryPublication == "failed" {
		w.Header().Set("X-Myctx-Telemetry-Publication", result.TelemetryPublication)
	}
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
		if result.Status != 0 && result.Status != http.StatusOK && result.Status != http.StatusNoContent {
			failure(w, 503, "ServiceUnavailable", "Service returned an invalid success status")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if result.Value == nil {
		failure(w, 503, "ServiceUnavailable", "Service returned no result")
		return
	}
	normalized, err := NormalizeOutput(op, result.Value)
	if err != nil {
		failure(w, 503, "ServiceUnavailable", "Service returned an invalid result")
		return
	}
	raw, err := json.Marshal(normalized)
	if err != nil || len(raw) > 4<<20 {
		failure(w, 503, "ServiceUnavailable", "Service returned an invalid result")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	status := result.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		failure(w, 503, "ServiceUnavailable", "Service returned an invalid success status")
		return
	}
	w.WriteHeader(status)
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
			if err := validateQuery(field, value); err != nil {
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
			decoded, err := decodeValue(value)
			if err != nil {
				return input, err
			}
			if err := validateContract(field.schema, decoded, name, 0); err != nil {
				return input, err
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
