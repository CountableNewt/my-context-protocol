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
	if err := e.owner(ctx, account, owner); err != nil {
		return Review{}, err
	}
	m, ref, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return Review{}, err
	}
	review := Review{OwnerDID: owner, ProjectID: project, Entries: []Entry{}}
	seen := map[string]bool{}
	for _, shardRef := range m.Refs {
		var shard manifest
		if err = e.fetch(ctx, owner, shardRef, &shard); err != nil {
			return Review{}, err
		}
		if shard.Type != Namespace+"manifestShard" || shard.ProjectID != project || len(shard.Refs) > 100 || shard.Digest != refDigest(shard.Refs) {
			return Review{}, errors.New("invalid manifest shard")
		}
		raw, _ := json.Marshal(shard)
		if shardRef.URI != "at://"+owner+"/"+shard.Type+"/"+atproto.StableKey(shard.Type, digest(raw)) {
			return Review{}, errors.New("manifest shard key does not match content identity")
		}
		for _, entityRef := range shard.Refs {
			if err = ctx.Err(); err != nil {
				return Review{}, err
			}
			var fields map[string]json.RawMessage
			if err = e.fetch(ctx, owner, entityRef, &fields); err != nil {
				return Review{}, err
			}
			entry, err := decodeEntity(project, fields)
			if err != nil {
				return Review{}, err
			}
			encoded, err := encode(project, entry)
			if err != nil {
				return Review{}, err
			}
			if entityRef.URI != "at://"+owner+"/"+encoded.Collection+"/"+encoded.Key {
				return Review{}, errors.New("canonical record key does not match content identity")
			}
			key := entry.Table + ":" + entry.ID
			if seen[key] {
				return Review{}, errors.New("duplicate canonical entry")
			}
			seen[key] = true
			d, _ := definition(entry.Table)
			review.HasTelemetry = review.HasTelemetry || hasTelemetry(entry, d)
			review.Entries = append(review.Entries, entry)
			if len(review.Entries) > MaxEntries {
				return Review{}, errors.New("canonical record count exceeds limit")
			}
		}
	}
	review, err = finalize(review)
	if err != nil {
		return Review{}, err
	}
	if review.Digest != m.Digest {
		return Review{}, errors.New("canonical reconstruction digest mismatch")
	}
	local, err := e.Inventory(ctx, account, owner, project)
	if err != nil {
		return Review{}, err
	}
	if local.Digest != review.Digest {
		return Review{}, ErrChanged
	}
	// Refuse a mixed-version audit if the owner switched the manifest while the
	// immutable referenced records were being read.
	_, latest, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return Review{}, err
	}
	if latest.CID != ref.CID {
		return Review{}, atproto.ErrConflict
	}
	return review, nil
}
