# Repository provider runtime

`Hub` dispatches existing control callbacks to GitHub or Tangled, using injected
account identity, service-token clients, PDS author attestation, and bounded sync.
It never derives linked identity from a repository owner field or a mutable handle.

Apply `MigrationSQL` explicitly through the central migration command. Native
repository bindings, leases and metadata operation checkpoints also need the
current-schema snapshot allowlist. No schema is applied at startup.

Tangled connection requires authenticated Bobbin owner/name discovery and branch
access verification. Native repository DID, declaration owner, linked actor and
branch are stored separately from legacy provider display fields. Existing
connections from another provider are not overwritten. Public visibility must be
proved by independent anonymous availability of the exact same manifest; denial
or ambiguity remains private. Sync and reconciliation always use actor-authorized
reads regardless of the caller's visibility input. Relinking an account requires
reauthorizing its old native connections.

Metadata writeback validates the existing compiled canonical document and source
checksum, reserves a durable operation, and performs authenticated tree-only Git
editing for public and private repositories. Persisted branch names and commit
times make Git retries deterministic. Native ticket/patch references are
checkpointed before user-PDS author attestation; a PDS outage retries attestation
without duplicating remote Git or knot operations. Success requires all stages.
Both current Git writers require a SHA-1 source repository; unsupported object
formats fail explicitly.

The knot assigns ticket keys, so a lost create acknowledgement is ambiguous.
The operation is marked before dispatch and automatic retry fails closed until
its external outcome is reconciled. Known failed patch submissions retain their
ticket references; acknowledged records are never recreated. An operation marker
is included in its ticket body for operator reconciliation. Do not reset uncertain
checkpoints merely to retry. A future reviewed reconciliation command must verify
matching native records before resolving these states.

`Reconcile(ctx, limit)` atomically claims at most twenty due bindings, checks
authenticated heads, and invokes the existing bounded sync callback. The source
checkpoint advances only after a confirmed `ready` sync. Failed work becomes due
after its bounded lease. The host schedules this routine; no background loop is
started and no queued goroutine is acknowledged as durable work.

Tests execute real SQLite SQL and cover ownership, provider isolation, native
identity binding, conservative visibility, private authenticated writeback,
operation retry/attestation failure, uncertain external outcomes, and sync
checkpoint recovery. Hosted OAuth, private mirror availability and native proposal
acceptance must be verified against the deployed Tangled service configuration.
