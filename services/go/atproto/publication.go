package atproto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Journal must durably reserve before returning, and atomically reject reuse of
// an owner/key with another digest. Complete must be idempotent. Pending intents
// must remain enumerable for a bounded reconciliation job after process death.
// Neither implementation nor projection updates may mark an intent complete
// until the canonical write has been confirmed. Secrets are not journal data.
type Journal interface {
	Reserve(ctx context.Context, owner, key, digest string, mutation Mutation) (completed *Reference, err error)
	Complete(ctx context.Context, owner, key, digest string, ref Reference) error
}

// RetirableJournal invalidates completed references before physical deletion so
// a future identical immutable publication verifies/recreates its PDS record.
type RetirableJournal interface {
	Retire(context.Context, string, Reference) error
}

type Publisher struct {
	Records Records
	Journal Journal
}

// Publish can safely resume after a lost HTTP response or interrupted journal
// acknowledgement. Conflicting concurrent writes stay conflicts. Validation and
// explicit publication consent must run before calling this method.
func (p Publisher) Publish(ctx context.Context, owner, key string, m Mutation) (Reference, error) {
	if p.Records == nil || p.Journal == nil || owner != m.Repo || key == "" {
		return Reference{}, errors.New("owner, idempotency key, records, and durable journal required")
	}
	if err := m.Validate(); err != nil {
		return Reference{}, err
	}
	// Own the byte slice so callers cannot mutate the durable intent mid-flight.
	m.Record = bytes.Clone(m.Record)
	if m.ExpectedCID != nil {
		cid := *m.ExpectedCID
		m.ExpectedCID = &cid
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return Reference{}, err
	}
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	done, err := p.Journal.Reserve(ctx, owner, key, digest, m)
	if err != nil {
		return Reference{}, err
	}
	if done != nil {
		if done.URI != m.URI() || !validCID(done.CID) {
			return Reference{}, errors.New("invalid completed publication reference")
		}
		return *done, nil
	}
	existing, err := p.Records.Get(ctx, m)
	var ref Reference
	if err == nil && equalRecord(existing.Value, m.Record) {
		ref = existing.Reference
	} else {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Reference{}, err
		}
		ref, err = p.Records.Put(ctx, m)
		if err != nil {
			return Reference{}, err
		}
	}
	if ref.URI != m.URI() || !validCID(ref.CID) {
		return Reference{}, errors.New("unconfirmed canonical record reference")
	}
	if err = p.Journal.Complete(ctx, owner, key, digest, ref); err != nil {
		return Reference{}, err
	}
	return ref, nil
}

func equalRecord(a, b []byte) bool {
	canonical := func(raw []byte) ([]byte, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	x, err := canonical(a)
	if err != nil {
		return false
	}
	y, err := canonical(b)
	return err == nil && bytes.Equal(x, y)
}
