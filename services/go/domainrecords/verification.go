package domainrecords

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
)

// Verify independently traverses canonical owner records without updating caches,
// cursors, projections or the PDS. It reports success only after exact digest
// comparison with the current local public projection. The caller's deadline
// bounds this audit; an interrupted audit is never reported as verified.
func (e Engine) Verify(ctx context.Context, account, owner, project string) (Review, error) {
	return e.verify(ctx, account, owner, project, nil)
}

func (e Engine) verify(ctx context.Context, account, owner, project string, tracker *verificationTracker) (Review, error) {
	if err := e.owner(ctx, account, owner); err != nil {
		return Review{}, err
	}
	m, ref, err := e.readManifest(ctx, owner, project)
	if err != nil {
		manifest := placeholder(owner, Namespace+"manifest", atproto.StableKey(Namespace+"manifest", project))
		return Review{}, tracker.failing(err, atproto.Reference{URI: manifest.URI()}, "ManifestReadFailed", errors.Is(err, ErrInvalidCanonical) || errors.Is(err, atproto.ErrInvalidResponse))
	}
	if tracker != nil && ref.URI != placeholder(owner, Namespace+"manifest", atproto.StableKey(Namespace+"manifest", project)).URI() {
		return Review{}, tracker.failing(atproto.ErrConflict, ref, "InvalidManifestIdentity", true)
	}
	tracker.checked(ref)
	review := Review{OwnerDID: owner, ProjectID: project, Entries: []Entry{}}
	seen := map[string]bool{}
	for _, shardRef := range m.Refs {
		var shard manifest
		if err = e.verificationFetch(ctx, owner, shardRef, &shard, tracker); err != nil {
			return Review{}, err
		}
		if shard.Type != Namespace+"manifestShard" || shard.ProjectID != project || len(shard.Refs) > 100 || shard.Digest != refDigest(shard.Refs) {
			return Review{}, tracker.failing(errors.New("invalid manifest shard"), shardRef, "InvalidManifestShard", true)
		}
		raw, _ := json.Marshal(shard)
		if shardRef.URI != "at://"+owner+"/"+shard.Type+"/"+atproto.StableKey(shard.Type, digest(raw)) {
			return Review{}, tracker.failing(errors.New("manifest shard key does not match content identity"), shardRef, "InvalidManifestShardIdentity", true)
		}
		tracker.checked(shardRef)
		for _, entityRef := range shard.Refs {
			if err = ctx.Err(); err != nil {
				return Review{}, err
			}
			var fields map[string]json.RawMessage
			if err = e.verificationFetch(ctx, owner, entityRef, &fields, tracker); err != nil {
				return Review{}, err
			}
			entry, err := decodeEntity(project, fields)
			if err != nil {
				return Review{}, tracker.failing(err, entityRef, "InvalidRecord", true)
			}
			encoded, err := encode(project, entry)
			if err != nil {
				return Review{}, tracker.failing(err, entityRef, "InvalidRecord", true)
			}
			if entityRef.URI != "at://"+owner+"/"+encoded.Collection+"/"+encoded.Key {
				return Review{}, tracker.failing(errors.New("canonical record key does not match content identity"), entityRef, "InvalidRecordIdentity", true)
			}
			key := entry.Table + ":" + entry.ID
			if seen[key] {
				return Review{}, tracker.failing(errors.New("duplicate canonical entry"), entityRef, "DuplicateRecord", true)
			}
			tracker.checked(entityRef)
			seen[key] = true
			d, _ := definition(entry.Table)
			review.HasTelemetry = review.HasTelemetry || hasTelemetry(entry, d)
			review.Entries = append(review.Entries, entry)
			if len(review.Entries) > MaxEntries {
				return Review{}, tracker.failing(errors.New("canonical record count exceeds limit"), entityRef, "RecordLimitExceeded", true)
			}
		}
	}
	review, err = finalize(review)
	if err != nil {
		return Review{}, tracker.failing(err, ref, "InvalidRecordGraph", true)
	}
	if review.Digest != m.Digest {
		return Review{}, tracker.failing(errors.New("canonical reconstruction digest mismatch"), ref, "ManifestDigestMismatch", true)
	}
	local, err := e.Inventory(ctx, account, owner, project)
	if err != nil {
		return Review{}, err
	}
	consent, _, err := e.state(ctx, account, project)
	if err != nil {
		return Review{}, err
	}
	if consent.Public && !consent.Telemetry {
		local, err = metadataOnlyReview(local)
		if err != nil {
			return Review{}, err
		}
	}
	if local.Digest != review.Digest {
		return Review{}, ErrChanged
	}
	// Refuse a mixed-version audit if the owner switched the manifest while the
	// immutable referenced records were being read.
	_, latest, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return Review{}, tracker.failing(err, ref, "ManifestReadFailed", errors.Is(err, ErrInvalidCanonical) || errors.Is(err, atproto.ErrInvalidResponse))
	}
	if latest.CID != ref.CID {
		tracker.failing(atproto.ErrConflict, ref, "ManifestChanged", false)
		if tracker != nil {
			tracker.issue.ActualCID = latest.CID
		}
		return Review{}, atproto.ErrConflict
	}
	return review, nil
}
