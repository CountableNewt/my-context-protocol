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
