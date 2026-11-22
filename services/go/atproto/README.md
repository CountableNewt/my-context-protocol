# Canonical publication primitives

This package is a foundation, not an enabled migration. Production domain
handlers still require explicit consent, schema validation, linked-owner
verification and projection integration before calling it.
No existing data is published by adding this package.

`Client` implements bounded `getRecord` / `putRecord` operations against an
already verified HTTPS PDS origin. Its authorizer is responsible for DID/session
binding and request-specific OAuth/DPoP; redirects are rejected. Identity
discovery must enforce SSRF protection before constructing the client. Record
validation here checks envelope identity only, not a published Lexicon schema.

`Mutation.ExpectedCID == nil` serializes `swapRecord: null`, requiring the record
to be absent. Updates supply the previous CID. `StableKey` produces deterministic
keys without revealing legacy identifiers. PDS responses must identify the exact
requested AT-URI and a nonempty CID.

`Publisher` reserves an intent before network activity. A durable `Journal`
implementation must atomically bind `(owner DID, idempotency key)` to its digest,
retain pending intents for reconciliation, and durably acknowledge completion.
`SQLJournal` implements this contract using an explicit additive migration;
`Pending` lists bounded owner-scoped reconciliation pages. Its private payloads
must never be included in public indexing. The interface deliberately has no
in-memory production fallback. Retrying an
interrupted request reads the deterministic record first; an equal canonical
record confirms a previously accepted write without duplicating it. Different
content remains subject to PDS compare-and-swap. A failure after remote commit
must stay pending until reconciliation confirms it.

Concurrent identical retries may receive `ErrConflict` when they race between
read and compare-and-swap; retrying converges. Successful publication does not
mean a Turso projection has caught up. Domain handlers must expose projection
status separately and never make a journal completion depend on an unsafe
uncoordinated SQL/PDS dual write.

`NewAuthenticatedClient` accepts a `DoWithAuth` executor for the OAuth SDK's
DPoP nonce/refresh loop and DID/session binding. The low-level `NewClient`
authorizer hook alone is insufficient for production OAuth lifecycle handling.

Not implemented here: OAuth discovery/session management,
record deletion, subscriptions/projection rebuild, schema publication, consent
UI, Spaces, or migrations of existing accounts. Those require application-level
integration and acceptance tests.
