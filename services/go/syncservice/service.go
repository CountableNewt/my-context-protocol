// Package syncservice runs bounded repository sync work in the request lifetime.
// It never acknowledges a queued background goroutine as durable work.
package syncservice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
)

var ErrBusy = errors.New("sync already running")
var ErrUnsupported = errors.New("sync requires functionality not yet ported")
var ErrNotFound = errors.New("repository connection not found")
var ErrInvalid = errors.New("repository validation failed")

type Connection struct{ ID, ProjectID, Owner, Repository, Branch, EncryptedToken, AccountEncryptedToken, WebhookSecret, InstallationID, PriorReleaseID string }
type Job struct {
	ID        string `json:"job_id"`
	ProjectID string `json:"project_id"`
	ReleaseID string `json:"release_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}
type Store interface {
	Connection(context.Context, string) (Connection, error)
	Connections(context.Context, string, string) ([]Connection, error)
	Claim(context.Context, Connection, string, time.Time) (Job, bool, error)
	Job(context.Context, string) (Job, error)
	UnsupportedOverrides(context.Context, string) (bool, error)
	Publish(context.Context, Connection, Job, string, []compiler.Compiled, compiler.Report, time.Time) error
	Fail(context.Context, Job, string, time.Time) error
}
type RuntimeOverrideStore interface {
	RuntimeOverride(context.Context, Connection, compiler.Skill, string) (map[string]any, error)
}

type CommitResolver interface {
	ResolveCommitSHA(context.Context, string, string, string, string) (string, error)
}

type Fetcher interface {
	Fetch(context.Context, string, string, string, string) (reposync.Outcome, error)
}
type Config struct {
	Store        Store
	Fetcher      Fetcher
	DecryptToken func(string) (string, error)
	// GitHub App installations must be resolved explicitly, never replaced silently
	// with an account token that may have different repository permissions.
	InstallationToken            func(context.Context, string, string) (string, error)
	InternalToken, WebhookSecret string
	WorkTimeout                  time.Duration
	MaxCompiledBytes             int
	MaxSkills                    int
}
type Service struct{ config Config }

func New(config Config) (*Service, error) {
	if config.Store == nil {
		return nil, errors.New("sync store is required")
	}
	if config.Fetcher == nil {
		config.Fetcher = reposync.Fetcher{}
	}
	if config.WorkTimeout <= 0 {
		config.WorkTimeout = 70 * time.Second
	}
	if config.WorkTimeout > 90*time.Second {
		return nil, errors.New("sync work timeout must not exceed 90 seconds with 120-second leases")
	}
	if config.MaxSkills <= 0 {
		config.MaxSkills = 5000
	}
	if config.MaxCompiledBytes <= 0 {
		config.MaxCompiledBytes = 16 << 20
	}
	return &Service{config}, nil
}
func (s *Service) SyncProject(ctx context.Context, projectID string) (map[string]any, error) {
	job, err := s.Run(ctx, projectID, "")
	return map[string]any{"job_id": job.ID, "release_id": job.ReleaseID, "status": job.Status, "error_code": job.ErrorCode}, err
}
func (s *Service) Run(ctx context.Context, projectID, key string) (job Job, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.WorkTimeout)
	defer cancel()
	connection, err := s.config.Store.Connection(ctx, projectID)
	if err != nil {
		return job, err
	}
	job, claimed, err := s.config.Store.Claim(ctx, connection, key, time.Now().UTC())
	if err != nil || !claimed {
		return job, err
	}
	defer func() {
		if err != nil {
			code := "sync_failed"
			switch {
			case errors.Is(err, ErrUnsupported):
				code = "unsupported_metadata"
			case errors.Is(err, ErrInvalid):
				code = "validation_failed"
			case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				code = "deadline_exceeded"
			}
			job.Status = "failed"
			job.ErrorCode = code
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if failErr := s.config.Store.Fail(cleanup, job, code, time.Now().UTC()); failErr != nil {
				err = errors.Join(err, failErr)
			}
		}
	}()
	unsupported, err := s.config.Store.UnsupportedOverrides(ctx, projectID)
	if err != nil {
		return job, err
	}
	if unsupported {
		return job, ErrUnsupported
	}
	token := ""
	for _, encrypted := range []string{connection.EncryptedToken, connection.AccountEncryptedToken} {
		if encrypted == "" {
			continue
		}
		if s.config.DecryptToken == nil {
			return job, ErrUnsupported
		}
		candidate, decryptErr := s.config.DecryptToken(encrypted)
		if decryptErr == nil && candidate != "" {
			token = candidate
			break
		}
	}
	if connection.InstallationID != "" {
		if s.config.InstallationToken == nil {
			return job, ErrUnsupported
		}
		token, err = s.config.InstallationToken(ctx, connection.InstallationID, token)
		if err != nil {
			return job, err
		}
	}
	snapshot, err := s.config.Fetcher.Fetch(ctx, connection.Owner, connection.Repository, connection.Branch, token)
	if err != nil {
		return job, err
	}
	defer snapshot.Cleanup()
	// Request one extra entry so truncation cannot silently publish an incomplete catalog.
	files, err := reposync.FindSkillFiles(ctx, snapshot.RepositoryRoot, s.config.MaxSkills+1)
	if err != nil {
		return job, err
	}
	if len(files) > s.config.MaxSkills {
		return job, reposync.ErrTooManyEntries
	}
	policies, err := compiler.Policies(snapshot.RepositoryRoot)
	if err != nil {
		return job, err
	}
	skills := make([]compiler.Skill, 0, len(files))
	report := compiler.Report{Valid: true, Errors: []compiler.Issue{}, Warnings: []compiler.Issue{}}
	compiledBytes := 0
	for _, filename := range files {
		if err = ctx.Err(); err != nil {
			return job, err
		}
		skill, e := compiler.ReadSkill(filename, snapshot.RepositoryRoot)
		if e != nil {
			report.Errors = append(report.Errors, compiler.Issue{Path: filename, Message: e.Error()})
			continue
		}
		compiledBytes += len(skill.Body) + len(skill.Description)
		if compiledBytes > s.config.MaxCompiledBytes {
			return job, errors.New("compiled catalog exceeds memory budget")
		}
		skills = append(skills, skill)
	}
	validated := compiler.Validate(skills)
	report.Errors = append(report.Errors, validated.Errors...)
	report.Warnings = append(report.Warnings, validated.Warnings...)
	report.Valid = len(report.Errors) == 0
	compiled := make([]compiler.Compiled, 0, len(skills))
	revision := snapshot.ResolvedCommitSHA
	if revision == "" {
		if resolver, ok := s.config.Fetcher.(CommitResolver); ok {
			revision, err = resolver.ResolveCommitSHA(ctx, connection.Owner, connection.Repository, connection.Branch, token)
			if err != nil {
				return job, err
			}
		}
	}
	if revision == "" {
		revision = "unknown"
	}
	for _, skill := range skills {
		var policy *compiler.Policy
		if p, ok := policies[skill.Name]; ok {
			policy = &p
		}
		var override map[string]any
		if provider, ok := s.config.Store.(RuntimeOverrideStore); ok {
			preferred := scalarMetadata(skill.Metadata["scope"])
			if preferred == "" {
				preferred = "task"
			}
			if policy != nil && (policy.BaseChecksum == "" || policy.BaseChecksum == skill.Hash) {
				if scope := scalarMetadata(policy.Metadata["scope"]); scope != "" {
					preferred = scope
				}
			}
			override, err = provider.RuntimeOverride(ctx, connection, skill, preferred)
			if err != nil {
				return job, err
			}
		}
		c, e := compiler.CompileWithOverride(skill, connection.Owner+"/"+connection.Repository, revision, policy, override)
		if e != nil {
			return job, e
		}
		c.Files, e = compiler.PackageFiles(ctx, filepath.Dir(filepath.Join(snapshot.RepositoryRoot, filepath.FromSlash(skill.Path))))
		if e != nil {
			return job, e
		}
		for _, file := range c.Files {
			compiledBytes += len(file.Content)
		}
		if compiledBytes > s.config.MaxCompiledBytes {
			return job, errors.New("compiled catalog exceeds memory budget")
		}
		compiled = append(compiled, c)
	}
	if err = s.config.Store.Publish(ctx, connection, job, revision, compiled, report, time.Now().UTC()); err != nil {
		return job, err
	}
	if !report.Valid {
		job.Status = "failed"
		job.ErrorCode = "validation_failed"
		return job, ErrInvalid
	}
	job.Status = "ready"
	return job, nil
}
func VerifySignature(secret, header string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(signature, mac.Sum(nil))
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		write(w, 200, map[string]any{"status": "ok", "service": "sync"})
		return
	}
	if r.URL.Path == "/webhooks/github" && r.Method == http.MethodPost {
		s.webhook(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/internal/sync") {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.config.InternalToken == "" || subtle.ConstantTimeCompare([]byte(supplied), []byte(s.config.InternalToken)) != 1 {
		write(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/internal/sync/jobs/") && r.Method == http.MethodGet {
		job, err := s.config.Store.Job(r.Context(), strings.TrimPrefix(r.URL.Path, "/internal/sync/jobs/"))
		if err != nil {
			respondError(w, err)
			return
		}
		write(w, 200, job)
		return
	}
	if r.URL.Path != "/internal/sync" || r.Method != http.MethodPost {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	var input struct {
		ProjectID      string `json:"project_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.ProjectID == "" || len(input.ProjectID) > 128 || len(input.IdempotencyKey) > 128 {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	job, err := s.Run(r.Context(), input.ProjectID, input.IdempotencyKey)
	if err != nil {
		respondError(w, err)
		return
	}
	status := 200
	if job.Status == "running" {
		status = 202
	}
	if job.Status == "failed" {
		status = 422
	}
	write(w, status, job)
}
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		write(w, 413, map[string]string{"error": "payload_too_large"})
		return
	}
	var payload struct {
		Ref        string `json:"ref"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &payload) != nil {
		write(w, 400, map[string]string{"error": "invalid_payload"})
		return
	}
	globalAuthenticated := VerifySignature(s.config.WebhookSecret, r.Header.Get("X-Hub-Signature-256"), body)
	if r.Header.Get("X-GitHub-Event") == "ping" && globalAuthenticated {
		write(w, 200, map[string]string{"status": "verified"})
		return
	}
	if event := r.Header.Get("X-GitHub-Event"); event != "push" && event != "ping" {
		write(w, 422, map[string]string{"error": "unsupported_event"})
		return
	}
	if payload.Repository.Name == "" || payload.Repository.Owner.Login == "" {
		write(w, 400, map[string]string{"error": "invalid_repository"})
		return
	}
	connections, err := s.config.Store.Connections(r.Context(), payload.Repository.Owner.Login, payload.Repository.Name)
	if err != nil {
		respondError(w, err)
		return
	}
	authenticated := globalAuthenticated
	targets := []Connection{}
	for _, connection := range connections {
		if !globalAuthenticated && !VerifySignature(connection.WebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
			continue
		}
		authenticated = true
		if !payload.Deleted && payload.Ref == "refs/heads/"+connection.Branch {
			targets = append(targets, connection)
		}
	}
	if !authenticated {
		write(w, 401, map[string]string{"error": "invalid_signature"})
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		write(w, 200, map[string]string{"status": "verified"})
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" || len(delivery) > 128 || strings.ContainsAny(delivery, "\r\n") {
		write(w, 400, map[string]string{"error": "invalid_delivery"})
		return
	}
	if len(targets) > 10 {
		write(w, 422, map[string]string{"error": "webhook_fanout_limit"})
		return
	}
	jobs := []Job{}
	responseStatus := http.StatusOK
	ctx, cancel := context.WithTimeout(r.Context(), s.config.WorkTimeout)
	defer cancel()
	for _, connection := range targets {
		job, e := s.Run(ctx, connection.ProjectID, "github:"+delivery)
		if e != nil {
			respondError(w, e)
			return
		}
		if job.Status == "failed" {
			write(w, 422, job)
			return
		}
		if job.Status == "running" {
			responseStatus = http.StatusAccepted
		}
		jobs = append(jobs, job)
	}
	write(w, responseStatus, map[string]any{"status": "processed", "jobs": jobs})
}
func write(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func respondError(w http.ResponseWriter, err error) {
	status, code := 500, "sync_failed"
	switch {
	case errors.Is(err, ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, ErrBusy):
		status, code = 409, "sync_in_progress"
	case errors.Is(err, ErrUnsupported):
		status, code = 501, "unsupported_metadata"
	case errors.Is(err, ErrInvalid):
		status, code = 422, "validation_failed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code = 504, "deadline_exceeded"
	}
	write(w, status, map[string]string{"error": code})
}

func scalarMetadata(value any) string { s, _ := value.(string); return s }
