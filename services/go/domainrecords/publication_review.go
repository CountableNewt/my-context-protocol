package domainrecords

import (
	"context"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// PublicationReview binds consent to exactly the data selected for publication.
// Inventory remains the complete operator view, including private telemetry.
func (e Engine) PublicationReview(ctx context.Context, account, owner, project string, includeTelemetry bool) (Review, error) {
	review, err := e.Inventory(ctx, account, owner, project)
	if err != nil || includeTelemetry {
		return review, err
	}
	return metadataOnlyReview(review)
}

func metadataOnlyReview(review Review) (Review, error) {
	entries := make([]Entry, 0, len(review.Entries))
	for _, entry := range review.Entries {
		d, err := definition(entry.Table)
		if err != nil {
			return Review{}, err
		}
		if d.Telemetry {
			continue
		}
		if entry.Table == "project_runtime_settings" {
			values := make(map[string]turso.Value, len(entry.Values))
			for key, value := range entry.Values {
				values[key] = value
			}
			values["telemetry_enabled"] = turso.Integer(0)
			entry.Values = values
		}
		entries = append(entries, entry)
	}
	review.Entries = entries
	review.HasTelemetry = false
	review.Warnings = append(append([]string(nil), review.Warnings...), "Publishing metadata without telemetry consent disables runtime telemetry. Existing local telemetry history remains private and is not published.")
	return finalize(review)
}
