# Controlled SQLite snapshot transfer

This tool reads a **local standalone SQLite backup** and writes only to an explicitly selected destination. It does not connect to Railway, stop services, change DNS, provision Turso, or cut over traffic. It uses Python's bundled SQLite reader and the existing Go Turso client; no production SQLite driver or new Go dependency is required.

The source must match every table, column, constraint and index in the embedded Swift baseline. Unknown or missing source objects, broken integrity/foreign keys, incomplete exports and unsupported typed values fail before destination initialization. SQLite internal objects are excluded. `_fluent_migrations` rows are copied unchanged along with every application table; the tool does not substitute template migration history.

## Prepare and export offline

Take the source with SQLite's backup API while application writes are paused for final cutover. Do not copy only a live `.sqlite` file while writes remain in its WAL. A copied standalone backup can be inspected independently before any destination credentials are configured. Existing nonempty adjacent WAL files are rejected by the exporter.

Run from `services/go` with Python 3 and Go installed:

```sh
umask 077
mkdir -p /private/tmp/mcp-sqlite-transfer
chmod 700 /private/tmp/mcp-sqlite-transfer

go run ./cmd/import export \
  --source /absolute/path/to/consistent-backup.sqlite \
  --output /private/tmp/mcp-sqlite-transfer/snapshot.jsonl

go run ./cmd/import inspect \
  --snapshot /private/tmp/mcp-sqlite-transfer/snapshot.jsonl
```

The artifact is atomically published with mode 0600; an existing output file is never replaced. It contains credentials and private data, so keep it private and out of git, logs, ordinary previews and public artifacts. Standard output contains only the snapshot fingerprint, table names/columns, counts, digests and encoded-size information. It never prints row contents.

Review the manifest before loading: the expected production snapshot currently has 30 application/history tables, three accounts and two projects. These are review expectations, not hardcoded defaults. Record the fingerprint and table-level counts/digests with the backup evidence. Maximum artifact size is 256 MiB; one encoded row must fit within 3 MiB. Oversized rows are rejected, never truncated.

## Load an empty, isolated destination

Provision a new Turso database without attaching it to running application services. Set its **destination** `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` through a private environment/secret mechanism; do not put credentials in command arguments. Keep the existing application encryption key unchanged for the eventual runtime, since encrypted account/repository tokens are copied byte for byte.

```sh
go run ./cmd/import load \
  --snapshot /private/tmp/mcp-sqlite-transfer/snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io \
  --initialize
```

`load` and `verify` require `--expected-host` to match the exact hostname in `TURSO_DATABASE_URL`. Replace the example hostname with an independently reviewed destination hostname; do not derive this guard from the same environment URL. URLs, userinfo, paths, ports, queries and wildcards are not accepted as the expected hostname. Export and inspect remain entirely local and need no host argument.

Initialization refuses an existing database. It applies the exact controlled Go baseline, then stages rows in foreign-key dependency order with enforcement enabled. Every chunk is an atomic transaction containing data inserts and its progress checkpoint. Transactions target 2 MiB and at most 120 insert statements, below Hrana limits; a larger permitted single row travels alone. There is no reset, replace, force or arbitrary SQL mode.

The import spans multiple transactions because the snapshot can exceed a single Hrana request. **Do not route traffic or allow another writer during staging.** The `go_snapshot_import` marker remains `loading` until every source table's exact count and typed multiset digest matches and destination foreign-key checks pass. Its final status must be `verified` before proceeding. No application table is deleted or overwritten by this tool.

If a request fails or its commit response is lost, rerun against the same destination and exact artifact:

```sh
go run ./cmd/import load \
  --snapshot /private/tmp/mcp-sqlite-transfer/snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io \
  --resume
```

Resume requires the same logical snapshot fingerprint, complete matching checkpoint metadata and matching digests for every already-staged prefix. Different snapshots, extra application rows, changed staged data and invalid progress are rejected before further inserts. A failure between baseline initialization and checkpoint setup can be recovered only when that initialized staging database is still exactly empty. Do not manually edit checkpoints to bypass validation.

## Verify, apply additive migrations and validate behavior

```sh
go run ./cmd/import verify \
  --snapshot /private/tmp/mcp-sqlite-transfer/snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io

# Baseline is already recorded. Add Go-owned state tables and maintenance indexes.
go run ./cmd/migrate

go run ./cmd/import verify \
  --snapshot /private/tmp/mcp-sqlite-transfer/snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io
```

`verify` is read-only. It compares every source table using exact typed values: signed 64-bit integers stay decimal strings in transport, BLOBs use base64 (including empty BLOBs), REAL timestamps preserve their IEEE-754 value, text/UUID spelling stays unchanged and NULL remains distinct. Digests are independent of row order and include duplicate rows. Target schema constraints are verified and foreign keys are never disabled. Only exact shipped Go additive schema objects can accompany the import. Resume and verification reject unknown tables, views, triggers and changed definitions of owned objects. Known Go additions can be applied after completion; their new tables are not misrepresented as source data.

A successful read-only verification can report `verified: true` while the marker still says `loading` if the previous final checkpoint response failed; use `load --resume` to finalize it. A later modification of application data makes the original snapshot comparison fail, so capture verification evidence **before** allowing runtime writes.

After transfer, complete authenticated Chrome, CLI, MCP/OAuth, encrypted GitHub token, repository sync and domain acceptance against the destination. Keep Railway and its backup intact until those checks and the final cutover are complete. This tool performs none of those external actions.

### Private local restore acceptance

The opt-in integration test restores a reviewed artifact into an ephemeral local SQLite database, verifies every typed source row before and after the shipped additive migrations, and calls MCP initialize, tools/list and mycontext_catalog using temporary synthetic API keys. It makes no network requests. It requires a private (0700) artifact parent, 0600 source.sqlite and artifact files, and matching TMPDIR. Source hashes must remain unchanged. Output contains aggregate counts only; a 0600 metadata-only acceptance report is saved in that parent. MCP requests may mutate telemetry in the temporary database after source-row verification.

```sh
TMPDIR=/private/tmp/mcp-production-rehearsal \
MCP_SQLITE_REHEARSAL_SNAPSHOT=/private/tmp/mcp-production-rehearsal/snapshot.jsonl \
go test -race ./snapshotimport -run TestApprovedLocalSnapshotRehearsal -v
```

Without the opt-in environment variable, this test skips. A passing local rehearsal does not change routing or perform a hosted transfer.

### Roll back after Go has accepted writes

The original pre-cutover backup cannot retain later writes. Before exporting a rollback snapshot, pause and drain **all** writers: dashboard/control mutations, MCP telemetry and key touches, OAuth/session callbacks, GitHub webhooks and sync, and maintenance/cron. Keep them stopped through export, restore, target verification and routing changes. Stop new ingress and let in-flight requests finish; changing only the frontend does not stop backend writers.

`export-remote` uses independent bounded SELECT requests, not a long-lived database transaction. `--writers-paused` is an operator declaration, not a lock this tool installs. The second count-and-typed-digest pass detects observed drift, including equal-count edits, but neither matching counts nor matching passes prove coherence if writers are active. Abort and repeat after a complete pause if that precondition is uncertain.

From `services/go`, using the independently reviewed exact source hostname and credentials provided securely in the environment:

```sh
# All application, webhook, callback and scheduled writers are stopped/drained.
# TURSO_DATABASE_URL and TURSO_AUTH_TOKEN now identify the current Go database.
go run ./cmd/import export-remote \
  --expected-host mycontext-production-OWNER.turso.io \
  --writers-paused \
  --output /private/tmp/mcp-sqlite-transfer/post-go-writes.jsonl

# This step is entirely offline and reads no database credentials.
go run ./cmd/import restore-local \
  --snapshot /private/tmp/mcp-sqlite-transfer/post-go-writes.jsonl \
  --output /private/tmp/mcp-sqlite-transfer/rollback.sqlite

# Independent offline inspection; should return the same snapshot_id/manifests.
go run ./cmd/import export \
  --source /private/tmp/mcp-sqlite-transfer/rollback.sqlite \
  --output /private/tmp/mcp-sqlite-transfer/rollback-checked.jsonl
```

Remote export accepts only the exact baseline schema plus shipped owned Go additions. It retains all baseline application rows, IDs, tokens, sessions, BLOBs, timestamps and Fluent migration history, including changes written by Go. It deliberately omits Go-only transient state and its migration/checkpoint registries. Both export and restore require new output paths and publish 0600 files atomically after verification; they never overwrite a database. Restore uses a private unpublished SQLite database, deferred foreign keys within one data transaction, integrity/FK checks, and an independent full typed re-export. Schema, type, size, FK or digest failures publish no output.

The tool does **not** upload the restored database to Railway, stop/start services, change routes or resume writers. Keep the original and post-write artifacts private. Separately review and authorize replacing the stopped Swift backend's SQLite file, ensure the original signing/encryption secrets remain configured, then validate authenticated Chrome/CLI/MCP/OAuth/sync behavior before reopening ingress. Keep Go writers stopped after switching back so the two databases cannot diverge. This local restore is a concrete data-preserving rollback artifact, not an automatic cutover.

The opt-in private rehearsal above now also edits a project in the temporary Go database, exports it with writers paused, restores it locally, and verifies that the edit survives while the original source and artifact hashes stay unchanged. Its private report includes only manifests, aggregate acceptance results and fingerprints.

### Recover a proven float transport fault in an unfinalized import

A remote JSON floating-point parameter parser can round a finite REAL argument by one IEEE-754 ULP. The importer now constructs REAL values from an exact signed integer significand and power-of-two arithmetic, avoiding JSON float parameters. Strict typed verification remains unchanged.

For an already loaded staging import that failed strict verification, `repair-staged-floats` is a narrowly scoped operator recovery tool. Keep the target unrouted and **all writers paused**. It requires the exact original artifact, an owned `loading` checkpoint, every table fully loaded with matching progress manifests, exact schema, matching counts/IDs/types/non-float values across every baseline table, and an independently supplied exact number of changed cells (maximum 32). Only one-ULP REAL drift qualifies. For every proposed cell, read-only SQL must reproduce the current stored value from the old JSON float binding and independently prove the new integer-based expression produces the source bits. Arbitrary one-ULP user edits that do not reproduce the transport fault are rejected.

```sh
go run ./cmd/import repair-staged-floats \
  --snapshot /private/tmp/mcp-sqlite-transfer/final-snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io \
  --writers-paused --apply --expected-float-cells 12

# Repair preserves loading status; strict normal resume verifies and finalizes.
go run ./cmd/import load \
  --snapshot /private/tmp/mcp-sqlite-transfer/final-snapshot.jsonl \
  --expected-host mycontext-production-OWNER.turso.io --resume
```

Do not copy the example count without independently auditing the target. All planned updates execute in one bounded transaction, guarded by original primary identity, REAL storage type, the exact observed SQLite decimal rendering, loading snapshot identity and the full completed progress manifest. A CHECK guard aborts the entire batch on a failed compare-and-swap; no partial repair is committed. Strict count/hash/FK verification follows, and the tool leaves the checkpoint loading until ordinary resume finalizes it. It never resets tables, changes non-REAL values or loosens verification. A lost transaction response is ambiguous: keep traffic and writers stopped, run strict verify, and review the result before retrying. This tool performs no deployment or routing actions.

Remote export and the read-only `verify` command use a dedicated trusted-query HTTP path that sends no transaction or connection-setting statements. This supports an operator-enforced Turso BlockWrites freeze, which also rejects `PRAGMA foreign_keys=ON`. The exporter still runs the read-only `PRAGMA foreign_key_check`; it does not depend on the connection's enforcement flag to inspect existing violations. The adapter refuses Batch entirely. Ordinary load, staged repair and runtime queries retain their existing foreign-key enforcement and transaction setup. The query-shape guard is not a general SQL sandbox; only the tool's fixed, owned inspection SQL may use this path.

## Current-schema recovery

Legacy v1 artifacts remain supported. `ExportRemote` with `CurrentSchema: true`
preserves every present shipped application table, including encrypted ATProto
OAuth requests/sessions, logout tombstones, and pending publication intents.
Artifacts containing additive tables use `mycontext-sqlite-snapshot-v2` and retain
strict schema allowlisting and typed row/count/digest verification. Local export
automatically recognizes these shipped schemas. Unknown or altered objects are
rejected; existing files are never replaced.

Use `go run ./cmd/import export-remote --current-schema --writers-paused
--expected-host <independently-reviewed-host> --output <new-private-artifact>`
for current Go recovery. The `--current-schema` flag is accepted only by remote
export; local exports detect the exact shipped schema automatically.

Only the migration registry and prior import checkpoints are reconstructed rather
than copied. The destination applies explicit shipped migrations before loading
current-schema rows. A current artifact must be restored into the Go application;
it is not a baseline Swift rollback artifact. Legacy baseline-only remote export
refuses nonempty ATProto tables, preventing accidental loss of private credentials
or unfinished publication work. Keep writers paused and drained throughout remote
export and verification, and preserve the encryption/signing configuration needed
to decrypt restored secrets. Snapshot files remain owner-readable private data;
never upload them to an ordinary preview or publish them as ATProto records.
