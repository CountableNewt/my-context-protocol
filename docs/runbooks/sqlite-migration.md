# PostgreSQL to durable SQLite

This procedure prepares a database artifact and a controlled Gateway cutover. It does not change
live Railway resources. Rehearse in Development with the exact Gateway revision before Production.
The source PostgreSQL service and its backups remain available throughout acceptance.

## Preconditions

- Use a single Gateway replica with a persistent volume mounted at `/data`. SQLite is local to
  that volume; multiple independent replicas cannot share its database safely.
- Build the Gateway revision whose migrations match the source database. The importer requires
  identical Fluent migration names and exact application table/column inventories.
- Use Python 3.10+ and `psql` locally. PostgreSQL export uses libpq `PGHOST`, `PGPORT`, `PGDATABASE`,
  `PGUSER`, `PGPASSWORD` or `PGSERVICE`; connection strings and passwords are never command arguments.
- Use a source role with complete visibility (table owner, superuser or `BYPASSRLS`). Export sets
  `row_security=off` so policies that would silently hide rows instead cause an export failure.
- Preserve `ENCRYPTION_KEY`, authentication secrets, GitHub and Stripe configuration. Existing
  encrypted credentials and sessions are copied and need the same keys to remain usable.
- Keep snapshots, database files and verification reports outside the repository. Snapshots include
  tokens, session payloads and private skill data. Both export and imported output use mode `0600`.

## Prepare and rehearse

1. Build the matching Gateway and create a fresh, empty SQLite schema in a private directory:

   ```bash
   cd services/mcp-gateway
   swift build -c release --product App
   APP_ENV=local DATABASE_DRIVER=sqlite SQLITE_PATH=/absolute/private/schema.sqlite \
     SEED_LOCAL_FIXTURES=0 DISABLE_ADMIN_ANALYTICS_ROLLUP_SCHEDULER=1 \
     DISABLE_SKILL_USAGE_RETENTION_SCHEDULER=1 DISABLE_STRIPE_RECONCILIATION_SCHEDULER=1 \
     .build/release/App migrate --yes
   ```

   Use a new absolute path. Do not start this database as a serving Gateway or seed fixtures into
   it. The schema template must contain Fluent migration history and no application rows. The
   importer reads it without modifying it. Keep both the template and Gateway at the same revision.

2. Configure libpq for the intended source environment. Verify the database and service identity
   before exporting; never reuse Production connection settings for a Development rehearsal.
   Run from the repository root:

   ```bash
   python3 scripts/migrate-postgres-to-sqlite.py export --output /absolute/private/source.jsonl
   python3 scripts/migrate-postgres-to-sqlite.py import \
     --snapshot /absolute/private/source.jsonl \
     --template /absolute/private/schema.sqlite \
     --output /absolute/private/gateway.sqlite > /absolute/private/verification.json
   ```

   Export uses one read-only repeatable-read transaction, with UTC timestamps and UTF-8 output.
   It exports every base table in `public`, including `app_sessions` and `_fluent_migrations`,
   without changing PostgreSQL. Import validates the complete table/column inventory and the
   exported row counts, preserves the template's own migration records, and converts PostgreSQL
   UUIDs to uppercase UUID text, booleans to integers, timestamps to Unix seconds and bytea to
   blobs. It verifies normalized row multiset hashes, declared foreign keys and SQLite integrity
   before atomically publishing a new file. It refuses to overwrite an existing output.

   Unsupported PostgreSQL types, extra tables, omitted rows, incompatible schemas and nonempty
   templates fail closed. Resolve the mismatch at the source or revise the reviewed converter;
   do not bypass verification or manually drop application data.

3. Start an isolated Gateway against the imported database with background integrations disabled
   or isolated. Verify real account/project lookup, a persisted browser/CLI session, encrypted
   repository credentials, skill catalog/resolution, analytics, and a write followed by restart.
   Compare `verification.json` counts with the source and inspect timestamp boundaries. Health
   alone is insufficient. Keep the rehearsal database separate from the final cutover artifact.

## Final cutover

1. Confirm the Gateway volume, single replica, backup policy and restore procedure in Railway.
   Retain PostgreSQL and its volume in config as code. Review `railway config plan` before any
   apply; applying a config that omits those resources deletes them.
2. Pause all source writers: stop the serving Gateway, webhook processing, retention/analytics
   jobs and any administrative clients. Keep them paused until the destination is selected.
   If remote shell/file access requires a running deployment, use a reviewed temporary maintenance
   deployment/start command that keeps a container alive **without launching App or its schedulers**.
   Disable its application healthcheck/routing for this maintenance window as appropriate. Do not
   assume a fully stopped Railway deployment still has an accessible runtime container.
   Export a fresh final snapshot and repeat the import/verification steps with new filenames.
   A rehearsal snapshot is stale once subsequent source writes occur.
3. In the Gateway service for the selected Railway environment, attach its dedicated volume at
   `/data` and verify the mount is present in the runtime container. Use the reviewed, authenticated
   file-transfer channel for that environment; a local file is not included in a GitHub deployment,
   and deployment images do not populate an existing mounted volume. Use the active maintenance
   container from step 2 to access the mount while the serving application and all writers are
   stopped. Copy the verified SQLite file onto its persistent volume as
   `/data/mycontext.sqlite`, readable and writable by the Gateway user. Verify a SHA-256 checksum
   before and after transfer (`shasum -a 256` locally, `sha256sum` in the Linux container). Confirm
   its owner matches the runtime user and mode is `0600`. Never copy only the main file of a running
   WAL database. Do not set `SQLITE_ALLOW_CREATE=1` as a substitute for transferring the artifact.
4. Set Gateway `DATABASE_DRIVER=sqlite` and `SQLITE_PATH=/data/mycontext.sqlite`. Keep
   `SQLITE_ALLOW_CREATE` unset: deployed startup must fail if the migrated file is missing.
   Retain the PostgreSQL connection variables for rollback; explicit SQLite selection overrides
   them. Preserve the same encryption/authentication keys and public origins.
5. Restore the normal Gateway start command and healthcheck/routing, then release the reviewed
   commit through the existing GitHub integration with Wait for CI enabled.
   Observe deployment success, the expected database path and authenticated acceptance. Verify a
   write survives a Gateway restart before reopening clients/webhooks. Re-enable scheduled work
   only on the selected Gateway. Repeat in Production only after Development acceptance.

## Backup and rollback

Back up live SQLite using its online backup API or the SQLite shell `.backup` command, then run
`PRAGMA integrity_check` against the backup. A main-file copy while WAL is active can omit committed
transactions. For a runtime image or maintenance container with the SQLite shell installed:

```bash
mkdir -p /data/backups
chmod 700 /data/backups
sqlite3 /data/mycontext.sqlite ".backup '/data/backups/mycontext-verified.sqlite'"
chmod 600 /data/backups/mycontext-verified.sqlite
sqlite3 -readonly /data/backups/mycontext-verified.sqlite 'PRAGMA integrity_check;'
```

Use unique backup filenames for each snapshot and copy them to storage outside the volume. The
Gateway image is not assumed to include `sqlite3`; use a reviewed maintenance image with the same
volume attached when the shell is unavailable. An attached volume alone is not an offsite backup.

For a restore rehearsal, place the backup on a separate test volume and run its Gateway with
`DATABASE_DRIVER=sqlite` and `SQLITE_PATH` pointing at that restored file. For a real restore, stop
all serving Gateway processes first (keep an isolated maintenance container active if needed for
volume access), move the existing database **and** `-wal`/`-shm` files into a recovery
directory, install the verified backup at `/data/mycontext.sqlite` with the correct owner/mode,
and restart one Gateway. Never leave WAL sidecars from the previous database beside a restored
main file. Verify the restored account/session and a restart before reopening writers.

Before the SQLite Gateway accepts writes, rollback is selecting `DATABASE_DRIVER=postgres` and the
previous Gateway deployment/configuration while writers remain paused. After SQLite accepts writes,
PostgreSQL is stale: stop writers and plan a verified reverse transfer before switching back. Do
not restore availability by silently discarding destination writes. Keep the PostgreSQL service,
volume, final snapshot and SQLite backups until the agreed acceptance/retention period ends.

## Local verification

```bash
python3 -m unittest discover -s scripts/tests -v
```

For the real PostgreSQL fixture test, start a disposable local PostgreSQL instance and set its
libpq connection variables plus `SQLITE_MIGRATION_TEST_PG=1`. The test creates and drops a randomly
named schema, so use a dedicated test database, never a shared environment:

```bash
SQLITE_MIGRATION_TEST_PG=1 python3 -m unittest discover -s scripts/tests -v
```
