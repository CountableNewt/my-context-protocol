package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/recordservice"
)

type CanonicalRecords interface {
	Scope(context.Context, string, string) (context.Context, error)
	WithEngine(context.Context, string, func(context.Context, string, *domainrecords.Engine) error) error
	IsCanonical(context.Context, string, string) (bool, error)
	Reconcile(context.Context, string, string) error
	Update(context.Context, string, string, []domainrecords.Change) error
	InspectConflict(context.Context, string, string) (*recordservice.ConflictReview, error)
	ResolveConflict(context.Context, string, string, string, string, string) error
}

func (s *Service) publication(r *http.Request, account, project string, parts []string) (any, int, error) {
	if s.cfg.Records == nil {
		return nil, 0, apiError{501, "ATProto record storage is not configured"}
	}
	if len(parts) == 1 && parts[0] == "conflict" {
		if r.Method == "GET" {
			review, err := s.cfg.Records.InspectConflict(r.Context(), account, project)
			if err != nil {
				return nil, 0, recordError(err)
			}
			if review == nil {
				return row{"status": "none"}, 200, nil
			}
			return review, 200, nil
		}
		if r.Method == "POST" {
			var body struct {
				IntentID   string `json:"intent_id"`
				CurrentCID string `json:"expected_current_cid"`
				Action     string `json:"action"`
			}
			if err := decode(r, &body); err != nil {
				return nil, 0, err
			}
			err := s.cfg.Records.ResolveConflict(r.Context(), account, project, body.IntentID, body.CurrentCID, body.Action)
			if errors.Is(err, domainrecords.ErrPending) {
				return row{"status": "pending"}, 202, nil
			}
			if err != nil {
				return nil, 0, recordError(err)
			}
			return row{"status": "complete"}, 200, nil
		}
		return nil, 0, missing("Not found")
	}

	// Verification deliberately bypasses Scope: it must not resume publication,
	// reconcile private projections or change readiness merely to inspect records.
	if len(parts) == 1 && parts[0] == "verify" {
		if r.Method != "POST" {
			return nil, 0, missing("Not found")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		report := domainrecords.VerificationReport{Status: "unavailable", ProjectID: project, Collections: []domainrecords.VerificationCollection{}, Issues: []domainrecords.VerificationIssue{}}
		err := s.cfg.Records.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
			report.OwnerDID = owner
			result, err := e.VerifyReport(ctx, account, owner, project)
			if err == nil {
				report = result
			}
			return err
		})
		if err != nil {
			report.Status = "unavailable"
			issue := domainrecords.VerificationIssue{Code: "RepositoryUnavailable", Message: "The linked repository could not be checked. Retry or reconnect the ATProto account."}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				report.Status = "incomplete"
				issue = domainrecords.VerificationIssue{Code: "VerificationInterrupted", Message: "Verification did not finish. Retry to check the complete record graph."}
			}
			report.Issues = []domainrecords.VerificationIssue{issue}
		}
		return report, 200, nil
	}

	// Reconciliation must remain available while an audit blocks normal reads.
	// Complete the owner repair audit before resuming any durable native intent.
	if len(parts) == 1 && parts[0] == "reconcile" && r.Method == "POST" {
		err := s.cfg.Records.WithEngine(r.Context(), account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
			return e.Audit(ctx, account, owner, project)
		})
		if err == nil {
			err = s.cfg.Records.Reconcile(r.Context(), account, project)
		}
		var pending *domainrecords.PendingError
		if errors.As(err, &pending) {
			return row{"status": "pending"}, 202, nil
		}
		if err != nil {
			return nil, 0, recordError(err)
		}
		return row{"status": "complete"}, 200, nil
	}
	ctx, err := s.cfg.Records.Scope(r.Context(), account, project)
	if err != nil {
		return nil, 0, recordError(err)
	}
	r = r.WithContext(ctx)
	var result any
	status := 200
	err = s.cfg.Records.WithEngine(r.Context(), account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {

		if len(parts) != 1 || parts[0] != "publication" {
			return missing("Not found")
		}
		includeTelemetry := false
		var consent domainrecords.Consent
		if r.Method == "GET" {
			if raw := r.URL.Query().Get("include_telemetry"); raw != "" {
				value, err := strconv.ParseBool(raw)
				if err != nil {
					return bad("Invalid telemetry review option")
				}
				includeTelemetry = value
			}
		} else if r.Method == "POST" {
			if err := decode(r, &consent); err != nil {
				return err
			}
			includeTelemetry = consent.Telemetry
		} else {
			return missing("Not found")
		}
		review, err := e.PublicationReview(ctx, account, owner, project, includeTelemetry)
		if err != nil {
			return err
		}
		if r.Method == "GET" {
			cursor, limit := 0, 25
			for name, target := range map[string]*int{"cursor": &cursor, "limit": &limit} {
				if raw := r.URL.Query().Get(name); raw != "" {
					n, err := strconv.Atoi(raw)
					if err != nil {
						return bad("Invalid publication page")
					}
					*target = n
				}
			}
			if cursor < 0 || cursor > len(review.Entries) || limit < 1 || limit > 100 {
				return bad("Invalid publication page")
			}
			end := min(cursor+limit, len(review.Entries))
			entries, err := json.Marshal(review.Entries[cursor:end])
			if err != nil {
				return err
			}
			if len(entries) > 2<<20 {
				return apiError{413, "Publication page is too large; reduce limit"}
			}
			result = row{"owner_did": owner, "project_id": project, "digest": review.Digest, "entry_count": len(review.Entries), "has_telemetry": review.HasTelemetry, "warnings": review.Warnings, "entries_json": string(entries)}
			if end < len(review.Entries) {
				result.(row)["next_cursor"] = end
			}
			return nil
		}
		if r.Method != "POST" {
			return missing("Not found")
		}
		ref, err := e.Publish(ctx, account, review, consent)
		var pending *domainrecords.PendingError
		if errors.As(err, &pending) {
			status = 202
			result = row{"status": "pending", "digest": pending.Digest, "completed": pending.Completed, "total": pending.Total}
			return nil
		}
		if err != nil {
			return err
		}
		result = row{"status": "complete", "digest": review.Digest, "uri": ref.URI, "cid": ref.CID}
		return nil
	})
	return result, status, recordError(err)
}

func recordError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domainrecords.ErrAuditInvalid) {
		return apiError{503, "Canonical records are missing or invalid; repair owner records and reconcile"}
	}
	if errors.Is(err, domainrecords.ErrConsent) {
		return apiError{409, "Review and explicitly authorize public data and telemetry publication"}
	}
	if errors.Is(err, domainrecords.ErrChanged) {
		return apiError{409, "Project changed; review publication again"}
	}
	if errors.Is(err, domainrecords.ErrNotMigrated) {
		return apiError{409, "Project has not been migrated to ATProto records"}
	}
	var pending *domainrecords.PendingError
	if errors.As(err, &pending) {
		return &pendingPublication{pending}
	}
	return err
}

type pendingPublication struct{ *domainrecords.PendingError }

func (p *pendingPublication) HTTPStatus() int { return 409 }
func (p *pendingPublication) HTTPPayload() any {
	return row{"error": "Canonical publication is pending; repeat this change to continue", "status": "pending", "digest": p.Digest, "completed": p.Completed, "total": p.Total}
}
