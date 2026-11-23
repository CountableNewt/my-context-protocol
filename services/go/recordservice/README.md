# Canonical SQL commands

`SQLProjection` evaluates supported application-owned statements in a native
SQLite transaction that always rolls back. It journals the exact SQL, private
arguments, canonical changes, expected source manifest and results privately.
Only confirmed owner-PDS publication permits the original SQL to commit with
canonical receipts. Unsupported SQL shapes fail closed. All constructors use the
unwrapped database to avoid recursive publication.

Interrupted commands resume before new commands. Private compare-and-swap guards,
aggregate effects and final public row values are checked in the commit
transaction. Git skill bodies remain private projections; source edits require a
repository change. A hydrated source snapshot is required before command capture.

## Reviewed conflict recovery

An external manifest generation can supersede a pending local command. The owner
must review `InspectConflict` and explicitly call `ResolveConflict` with the
intent ID, current remote manifest CID and action `discard`. The service reads the
owner PDS again, rejects stale reviews, and refuses discard while the source or
intended target is still resumable. An unavailable or malformed PDS response never
permits discard. No automatic abandonment or merge occurs.

Discard abandons unapplied local SQL and clears its duplicate private payload;
it does not retract already published records. The service then reconstructs the
current owner manifest. Pending private provider configuration is lost on discard
and can require reconnecting the repository. The PDS read and local transaction
cannot lock each other; subsequent remote edits are handled by normal
reconciliation. Failed reconciliation remains retryable after abandonment.

## Bounded maintenance

Control enables `CanonicalMaintenance` to exclude record-backed public rows from
raw global cleanup and retain jobs/locks referenced by unfinished publication.
Its `ExtraMaintenance` callback calls `Service.Maintenance` with bounded project
and row limits. Owner failures are deferred independently; a private visit cursor
prevents reconnect-required owners from starving other projects. Telemetry
retention runs through the same durable publication path before SQL deletion.
Retired record versions are collected with owner authorization, repository-head
CAS and current-manifest verification. Retention cannot recall replicated copies.
