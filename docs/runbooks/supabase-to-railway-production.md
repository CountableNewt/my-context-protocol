# Supabase to Railway production migration

> Historical migration record. The referenced Swift gateway, Railway configuration and one-time scripts were removed after the Go/Vercel/Turso cutover. These are not current deployment instructions; use `services/go/snapshotimport/README.md` for active snapshot tooling. Retrieve retired source from Git history when investigating this record.

This runbook records the 2026-08-10 production cutover from Fly, Supabase, and Vercel to Railway.
Railway project `MyContextProtocol` uses an isolated `production` environment containing Web,
Gateway, and Postgres.

The rollback-retention window ended on 2026-08-10 after production verification. The former Fly
app `my-context-protocol-prod-gateway`, Vercel project `my-context-protocol`, and Supabase project
`jjllqcgjfbakgmopnuli` were permanently deleted. Railway is the only hosted production stack.

## Safety boundaries

- Pause every Fly gateway writer before the final dump and keep Railway Gateway stopped until the
  restore completes.
- Require `MIGRATION_WRITERS_PAUSED=YES` and the exact target-reset confirmation accepted by
  `scripts/migrate-supabase-to-railway.sh`.
- Apply DNS only after the restored database and Railway deployments are healthy through their
  generated domains.
- Retain Fly machines, certificates, secrets, the Supabase project, and the Vercel deployment during
  the validation window. Do not let both databases accept writes.
- Stage Railway ownership and ACME records before changing traffic records.

## Migration

Use PostgreSQL 17-or-newer client tools and a Supabase direct or session-pooler URL on port 5432.
Never print either database URL.

```bash
SUPABASE_SOURCE_DATABASE_URL='postgres://...' \
RAILWAY_PROJECT_ID='3647f696-1766-459a-ac3f-0482e5a1f26c' \
RAILWAY_ENVIRONMENT=production \
MIGRATION_WRITERS_PAUSED=YES \
MIGRATION_CONFIRM_TARGET_RESET='MyContextProtocol/production/Postgres' \
bash scripts/migrate-supabase-to-railway.sh migrate
```

The script stores checksums and schema/count evidence under
`.migration-artifacts/supabase-to-railway-production/`, which is ignored by Git.

## DNS cutover

Railway owns these production routes:

| Host | Railway service |
| --- | --- |
| `mycontextprotocol.dev` | Web |
| `api.mycontextprotocol.dev` | Gateway |
| `mcp.mycontextprotocol.dev` | Gateway |
| `*.mcp.mycontextprotocol.dev` | Gateway |
| Tenant custom domains such as `mcp.samclemente.me` | Gateway |

Read the current target values from `railway domain status`; do not copy stale generated targets
from this document. In Marque, review the full transaction before applying it. Confirm Railway
reports every domain verified and a valid certificate, then test Web, API health, OAuth redirects,
the SaaS MCP base/wildcard, and each inventoried tenant domain.

The replaced Fly/Vercel traffic records used a 3600-second TTL. Railway documents certificate
issuance as normally completing within an hour after DNS is updated, so keep the maintenance window
open until old recursive-cache entries expire and every hostname passes TLS without a bypass.

## Recovery after decommission

The hosted Fly, Supabase, and Vercel rollback targets no longer exist. Recovery must use Railway
deployments/backups or a new environment restored from a retained dump. The final migration dump is
stored locally at `.migration-artifacts/supabase-to-railway-production/public-schema.dump`; at
decommission time its SHA-256 was
`dc9895e9ad4c236f1b8405cfcc5cf4a3aaa4ce899ca72e915a7a5b2b58e9b99c` and `pg_restore --list`
validated its archive catalog.

The production Fly ownership TXT records were removed from Marque after the Fly app was deleted.
Development ownership records for the separately migrated development environment were left
untouched.
