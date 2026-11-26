package domainrecords

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type VerificationCollection struct {
	Collection     string `json:"collection"`
	CheckedRecords int    `json:"checked_records"`
}
type VerificationIssue struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	URI         string `json:"uri,omitempty"`
	ExpectedCID string `json:"expected_cid,omitempty"`
	ActualCID   string `json:"actual_cid,omitempty"`
}
type VerificationReport struct {
	Status         string                   `json:"status"`
	OwnerDID       string                   `json:"owner_did"`
	ProjectID      string                   `json:"project_id"`
	CheckedRecords int                      `json:"checked_records"`
	Collections    []VerificationCollection `json:"collections"`
	Issues         []VerificationIssue      `json:"issues"`
}
type verificationTracker struct {
	report  VerificationReport
	seen    map[string]bool
	counts  map[string]int
	issue   VerificationIssue
	invalid bool
}

func (v *verificationTracker) checked(ref atproto.Reference) {
	if v == nil || v.seen[ref.URI] {
		return
	}
	v.seen[ref.URI] = true
	parts := strings.Split(ref.URI, "/")
	if len(parts) != 5 {
		return
	}
	v.counts[parts[3]]++
	v.report.CheckedRecords++
}
func (v *verificationTracker) failing(err error, ref atproto.Reference, code string, invalid bool) error {
	if v != nil {
		v.issue = VerificationIssue{Code: code, URI: ref.URI, ExpectedCID: ref.CID}
		v.invalid = invalid
	}
	return err
}
func (e Engine) verificationFetch(ctx context.Context, owner string, ref atproto.Reference, out any, tracker *verificationTracker) error {
	if tracker == nil {
		return e.fetch(ctx, owner, ref, out)
	}
	// Keep the exact fetched CID available in a mismatch report without issuing a
	// second read that could observe a different record version.
	capture := &verificationRecords{Records: e.Records}
	copy := e
	copy.Records = capture
	if err := copy.fetch(ctx, owner, ref, out); err != nil {
		tracker.failing(err, ref, "RecordReadFailed", errors.Is(err, ErrInvalidCanonical) || errors.Is(err, atproto.ErrInvalidResponse) || errors.Is(err, atproto.ErrConflict))
		if capture.record.URI == ref.URI && capture.record.CID != ref.CID {
			tracker.issue.Code = "RecordCIDMismatch"
			tracker.issue.ActualCID = capture.record.CID
		}
		return err
	}
	return nil
}

type verificationRecords struct {
	atproto.Records
	record atproto.Record
}

func (r *verificationRecords) Get(ctx context.Context, m atproto.Mutation) (atproto.Record, error) {
	value, err := r.Records.Get(ctx, m)
	r.record = value
	return value, err
}

// VerifyReport performs the same independent, read-only traversal as Verify and
// returns a safe summary. A partial traversal is never represented as verified.
func (e Engine) VerifyReport(ctx context.Context, account, owner, project string) (VerificationReport, error) {
	tracker := &verificationTracker{report: VerificationReport{Status: "incomplete", OwnerDID: owner, ProjectID: project, Collections: []VerificationCollection{}, Issues: []VerificationIssue{}}, seen: map[string]bool{}, counts: map[string]int{}}
	if err := e.owner(ctx, account, owner); err != nil {
		return tracker.report, err
	}
	owned, err := e.DB.Execute(ctx, statement("SELECT id FROM projects WHERE id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return tracker.report, err
	}
	if len(owned.Rows) != 1 {
		return tracker.report, errors.New("project is not owned by account")
	}
	consent, _, err := e.state(ctx, account, project)
	if err != nil {
		return tracker.report, err
	}
	_, err = e.verify(ctx, account, owner, project, tracker)
	for collection, count := range tracker.counts {
		tracker.report.Collections = append(tracker.report.Collections, VerificationCollection{collection, count})
	}
	sort.Slice(tracker.report.Collections, func(i, j int) bool {
		return tracker.report.Collections[i].Collection < tracker.report.Collections[j].Collection
	})
	if err == nil {
		tracker.report.Status = "verified"
		return tracker.report, nil
	}
	issue := tracker.issue
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		tracker.report.Status = "incomplete"
		issue.Code = "VerificationInterrupted"
		issue.Message = "Verification did not finish. Retry to check the complete record graph."
	case errors.Is(err, ErrChanged):
		tracker.report.Status = "out_of_sync"
		issue.Code = "ProjectionMismatch"
		issue.Message = "Published records do not match the current local public metadata."
	case errors.Is(err, atproto.ErrNotFound):
		tracker.report.Status = "missing"
		issue.Code = "RecordMissing"
		issue.Message = "A required published record is missing from the owner's repository."
		manifestURI := placeholder(owner, Namespace+"manifest", atproto.StableKey(Namespace+"manifest", project)).URI()
		if issue.URI == manifestURI && !consent.Public {
			tracker.report.Status = "not_published"
			issue.Code = "ProjectNotPublished"
			issue.Message = "This project has not published its canonical record manifest."
		}
	case tracker.invalid:
		tracker.report.Status = "invalid"
		if issue.Code == "" {
			issue.Code = "InvalidRecord"
		}
		issue.Message = "A published record failed its identity, content or CID verification."
	case errors.Is(err, atproto.ErrConflict):
		tracker.report.Status = "incomplete"
		issue.Code = "ManifestChanged"
		issue.Message = "The project manifest changed during verification. Retry against its current version."
	default:
		tracker.report.Status = "unavailable"
		issue.Code = "RepositoryUnavailable"
		issue.Message = "The repository could not be checked. Retry when its PDS is available."
	}
	tracker.report.Issues = append(tracker.report.Issues, issue)
	return tracker.report, nil
}
