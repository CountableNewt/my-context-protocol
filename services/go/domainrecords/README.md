# Canonical domain records

`Registry()` is a rigid allowlist of public columns, record types, SQL ownership
selectors and scalar types. `Schemas()` returns corresponding direct-field
Lexicons plus the manifest contracts. It includes projects, repository metadata
and native Tangled identities, releases, frontmatter, policy/override settings,
assignments, capabilities/routing/tools, validation, request/usage telemetry,
feedback and usage-report deduplication. SQL REAL values become normalized decimal
strings because ATProto records cannot contain floats. JSON frontmatter/policy
fields retain their exact strings, including unknown authored properties.

Git remains authoritative for source files. Source bodies, diffs and package
blobs are not copied into public records. Tokens, webhook secrets, domain proofs,
authentication configuration, sessions, API keys and security/admin controls are
not in the allowlist. Derived embeddings and aggregate analytics remain indexes.
Free-text metadata and feedback can still contain sensitive information: a reviewed
snapshot and explicit public/telemetry consent are required before migration.

## Publication and mutation

- `Inventory(account, DID, project)` checks the current linked identity and project
  owner, and produces a deterministic review digest. `Publish` re-inventories
  before publication and before the final commit; stale consent is rejected.
- Immutable entity versions retain legacy IDs and provenance. Each key hashes the
  collection, project ID, legacy ID and serialized record digest. Shards contain
  100 strong references; the stable per-project manifest holds up to 1000 shards.
  Its CID-conditional write is the publication/activation boundary.
- `Update` reads the current manifest and only uses SQL as its index when the
  confirmed local CID matches. Only changed immutable entities and shards are
  published. `ApplyChanges` previews the same merge without mutating its input.
- Writes and external rebuild reads are bounded by `WorkLimit` (default 25), with
  durable cursors. `PendingError` means the caller must resume the same operation;
  it must never be represented as a completed mutation. Completed prefixes are
  skipped without re-reading or re-publishing every prior PDS record.
- `Reconcile` reuses the local index for an unchanged manifest CID and incrementally
  rebuilds externally changed manifests. `Import` accepts explicit consent to
  reconstruct a missing local project. Both validate owner, immutable references,
  digest, required fields, public columns and cross-project parent relationships.

## Projection and application integration

Generic projection stages rows privately and applies all public table changes in
one bounded SQL transaction using `INSERT SELECT`, preserving private columns on
updates. Rows absent from the manifest are removed in dependency order. Ownership
and globally colliding legacy IDs are guarded transactionally. Catalog revisions
advance with the projection. Private staging and canonical receipts are applied by
explicit `MigrationSQL`, never by service startup.

When rebuilding Git-backed releases, `Hydrate` can reconstruct source bodies from
the pinned revision and verify their checksums. Active release pointers remain
unchanged until required bodies exist. A missing body returns
`ErrHydrationRequired`; retry reconciliation after hydration.

The application's durable native SQL bridge uses `BeforePublish` to persist the
prepared digest, `ExpectedPriorCID` / `ExpectedDigest` to reject stale intents,
and `NativeProjection` to return evaluated, privately persisted SQL. The hook must
include its own source-CID and intent-state guards and must never accept arbitrary
user SQL. It bypasses generic table rewrites so native ordering, private source
files, locks and catalog changes commit with the canonical receipt. Lost manifest
acknowledgements are confirmed against the durable reference plan before applying
native effects; retries do not merge stale intent changes onto unrelated updates.

`Collect(account, DID, project, limit)` performs bounded physical retirement of
unreferenced immutable PDS versions. Current generations and incomplete publication
plans remain protected. The optional `atproto.RepositoryDeleter` captures a repo
head, confirms the current manifest, and deletes using both `swapRecord` and
`swapCommit`; a concurrent external repository write conflicts instead of deleting
a newly referenced record. Lost delete acknowledgements are safely retryable.
Call it after publishing retention removals, under the owner's publication lease.
Deleting PDS records still cannot recall already replicated public copies.

Generic rebuilds preserve the old active release's metadata tree while source
hydration is pending, and prune it only with the final pointer change. Callers
must coordinate the owner's refresh/publication lease, consent renewal, readiness
of runtime reads during rebuilds, and domain verification.

`Audit` independently checks immutable references when the owner's repository
head changes, even if the project manifest CID is unchanged. Calls resume a
bounded durable cursor. Missing or malformed owner records set a private
readiness gate; publication and scoped mutations remain blocked until an owner
repair passes a complete audit. Pending repair does not clear that gate. Audit
never republishes missing records from SQL. Maintenance audits before entering
mutation scope so a repaired owner graph can recover without a bypass.
