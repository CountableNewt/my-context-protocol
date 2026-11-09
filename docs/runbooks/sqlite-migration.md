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
     SEED_LOCAL_FIXTURES=0 .build/release/App migrate --yes
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
   Export a fresh final snapshot and repeat the import/verification steps with new filenames.
   A rehearsal snapshot is stale once subsequent source writes occur.
3. While the Gateway is stopped, copy the verified SQLite file onto its persistent volume as
   `/data/gateway.sqlite`, readable and writable by the Gateway user. Verify a SHA-256 checksum
   before and after transfer. Never copy only the main file of a running WAL database.
4. Set Gateway `DATABASE_DRIVER=sqlite` and `SQLITE_PATH=/data/gateway.sqlite`. Keep
   `SQLITE_ALLOW_CREATE` unset: deployed startup must fail if the migrated file is missing.
   Retain the PostgreSQL connection variables for rollback; explicit SQLite selection overrides
   them. Preserve the same encryption/authentication keys and public origins.
5. Release the reviewed commit through the existing GitHub integration with Wait for CI enabled.
   Observe deployment success, the expected database path and authenticated acceptance. Verify a
   write survives a Gateway restart before reopening clients/webhooks. Re-enable scheduled work
   only on the selected Gateway. Repeat in Production only after Development acceptance.

## Backup and rollback

Back up live SQLite using its online backup API or the SQLite shell `.backup` command, then run
`PRAGMA integrity_check` against the backup. A main-file copy while WAL is active can omit committed
transactions. Test restoration to a separate volume before retiring the old source.

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
