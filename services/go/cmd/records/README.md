# Explicit canonical record migration

Run from `services/go` with the intended environment's `TURSO_DATABASE_URL`,
`TURSO_AUTH_TOKEN`, ATProto OAuth configuration and linked account credentials.
Production and preview credentials must remain separate. This command does not
apply schema migrations; run the explicit migration command first.

Inventory is the default and performs no PDS or projection writes. It reads only
public record fields, includes stable legacy row IDs, provenance, warnings and
the exact publication digest, and omits credentials and Git file bodies:

```sh
go run ./cmd/records --account ACCOUNT_UUID --project PROJECT_UUID \
  --review-file /private/tmp/project-publication-review.json
```

The optional review file is created with mode `0600` and never overwritten.
Review its complete contents for sensitive metadata and feedback. Product records
are public; deletion cannot recall replicated copies. Connecting a private
repository and prior telemetry opt-in do not authorize public publication.

Publish only after the account owner explicitly authorizes the reviewed data:

```sh
go run ./cmd/records --account ACCOUNT_UUID --project PROJECT_UUID \
  --publish --review-digest EXACT_REVIEWED_DIGEST --public-consent
```

If the inventory contains telemetry, publication also requires the separate
`--telemetry-consent` flag. The command never assumes that permission. Any
inventory change invalidates the digest. Each call does bounded publication work
under the account's authorization lease. A JSON `status: pending` means that the
canonical manifest is not yet fully published; rerun the identical reviewed
command to resume durable, idempotent writes. Do not treat a pending response as
completion or generate a replacement digest to bypass review.

Rebuild projections only from validated owner records, then independently audit:

```sh
go run ./cmd/records --account ACCOUNT_UUID --project PROJECT_UUID --reconcile
go run ./cmd/records --account ACCOUNT_UUID --project PROJECT_UUID --verify
```

Reconciliation is bounded and may return pending; repeat it to continue. Verify
is read-only: it reads immutable record versions, checks owner/project/content
identity and CIDs, reconstructs the entire manifest digest, compares the local
public projection, and rereads the remote manifest to reject a concurrent change.
Only a completed audit reports `verified`. Large catalogs may exceed the current
70-second authorization lease; an interrupted audit fails and must not count as
reconstruction acceptance. Verification does not implicitly reconcile drift.

Retain an environment snapshot before migration or cutover. Recovery after new
canonical writes must reconcile those records; an old SQL snapshot alone is not
a complete rollback. This tool never switches traffic or accounts automatically.
