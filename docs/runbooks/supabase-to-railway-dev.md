# Supabase to Railway development migration

> Historical migration record. The referenced Swift gateway, Railway configuration and one-time scripts were removed after the Go/Vercel/Turso cutover. These are not current deployment instructions; use `services/go/snapshotimport/README.md` for active snapshot tooling. Retrieve retired source from Git history when investigating this record.

This runbook records the completed development cutover from Fly, Supabase, and Vercel to Railway.
Production is documented separately and is also Railway-only.

## Deprecation status

Verified on 2026-08-10:

- `testing.mycontextprotocol.dev`, `api.testing.mycontextprotocol.dev`,
  `mcp.testing.mycontextprotocol.dev`, and `*.mcp.testing.mycontextprotocol.dev` resolve to Railway.
- Railway reports active Web, Gateway, Postgres, custom-domain, and wildcard-domain resources.
- Public Web and Gateway health checks return HTTP 200.
- The guarded migration artifacts show identical source and target table inventories and row counts.
- Railway Gateway uses discrete Railway Postgres variables and has no `DATABASE_URL` or
  `SUPABASE_DB_URL` binding.
- The former Fly app, Supabase project, and Vercel deployment were permanently removed after the
  Railway cutover was verified. They are not rollback targets.

## Railway development stack

Create one Railway project with a `dev` environment and three services:

- `Gateway`, built from `/railway/gateway.json`
- `Web`, built from `/railway/web.json`
- `Postgres`, using Railway's managed Postgres image and persistent volume

Set the Gateway's `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_USERNAME`, `DATABASE_PASSWORD`, and
`DATABASE_NAME` to references for the corresponding `Postgres.PG*` variables. Do not set
`DATABASE_URL` or `SUPABASE_DB_URL`: this app's URL-based driver path enables TLS and rejects
Railway's internal certificate, while its discrete private-network path explicitly disables TLS.
A public TCP proxy is created temporarily only while the migration runs.

The initial review deployment may be uploaded with `railway up`. GitHub autodeploy should not be
connected until these changes are on the `dev` branch.

## Database copy

Install PostgreSQL 17-or-newer client tools plus `jq`, and use a Supabase direct or session-pooler
URL on port 5432. Never use the transaction pooler on port 6543 for dump/restore.

The migration copies only the application-owned `public` schema. Supabase-managed auth, storage,
realtime, roles, ownership, and grants are intentionally excluded. The Gateway first proves it can
bootstrap the Railway database, then must be stopped before import. The guarded restore replaces
only the disposable Railway dev database's `public` schema.

```bash
export SUPABASE_SOURCE_DATABASE_URL='postgresql://...'
export RAILWAY_PROJECT_ID='...'
export RAILWAY_ENVIRONMENT=dev

bash scripts/migrate-supabase-to-railway.sh preflight

railway down --yes --service Gateway --environment dev
export MIGRATION_WRITERS_PAUSED=YES
export MIGRATION_CONFIRM_TARGET_RESET='MyContextProtocol/dev/Postgres'
bash scripts/migrate-supabase-to-railway.sh migrate
railway up --detach --service Gateway --environment dev
```

The script writes a protected custom-format dump and verification files beneath
`.migration-artifacts/supabase-to-railway-dev/`. It verifies exact public table inventories and row
counts after restore using one database session per side so temporary Railway proxy connections are
not churned once per table. Restore uses one worker by default because parallel workers can be
interrupted by the temporary proxy. The Gateway's next start applies any migrations newer than the
Supabase snapshot. Keep the old Supabase development database unchanged through review.

## Runtime variables

Copy the existing development runtime variables to `Gateway`, changing only platform bindings:

- `DATABASE_HOST=${{Postgres.PGHOST}}`
- `DATABASE_PORT=${{Postgres.PGPORT}}`
- `DATABASE_USERNAME=${{Postgres.PGUSER}}`
- `DATABASE_PASSWORD=${{Postgres.PGPASSWORD}}`
- `DATABASE_NAME=${{Postgres.PGDATABASE}}`
- `APP_ENV=dev`
- `HOST=0.0.0.0`
- `PORT=8080`

Point `Web.NEXT_PUBLIC_API_URL` at the Railway Gateway review domain and set
`NEXT_PUBLIC_APP_ENV=dev`. Use temporary Railway domains until review is complete; changing the
public `testing.mycontextprotocol.dev` or `api.testing.mycontextprotocol.dev` DNS is a separate
cutover step.

## Development DNS cutover

`mycontextprotocol.dev` is authoritative on Marque (`*.mqdns.*` nameservers), not Cloudflare. Use
the Marque CLI to stage and review one DNS transaction before applying it. Keep the records DNS-only
and use the Railway targets returned by `railway domain status` rather than copying stale targets
from this document.

The development cutover replaces these traffic records:

- `testing` CNAME: Vercel to the Railway `Web` target
- `api.testing` A and AAAA: remove, then add a CNAME to the Railway `Gateway` target
- `mcp.testing` A and AAAA: remove, then add a CNAME to the Railway `Gateway` target
- `*.mcp.testing` A: remove, then add a CNAME to the Railway `Gateway` wildcard target

Before changing traffic, add Railway's TXT verification records at `_railway-verify.testing`,
`_railway-verify.api.testing`, and `_railway-verify.mcp.testing`, and point
`_acme-challenge.mcp.testing` at Railway's wildcard authorization target. Wait until every custom
domain reports a valid certificate.

At cutover, set Gateway's `CORS_ORIGIN`, `FRONTEND_URL`, `GITHUB_OAUTH_REDIRECT_URI`,
`WEBHOOK_BASE_URL`, and `SESSION_COOKIE_DOMAIN` to the public development hostnames. Rebuild Web
with `NEXT_PUBLIC_APP_URL=https://testing.mycontextprotocol.dev` and
`NEXT_PUBLIC_API_URL=https://api.testing.mycontextprotocol.dev`.

## Review gates

- `GET /health` on Gateway returns 200.
- Web root and `/api/health` return 200.
- Gateway startup logs show all Fluent migrations current and no PostgreSQL/TLS errors.
- Public table names and row counts match Supabase.
- GitHub sign-in callback, session cookie sharing, repo sync, webhook delivery, and one MCP request
  work through the Railway review domains.
- The platform wildcard `*.mcp.testing.mycontextprotocol.dev` is added to Gateway and Railway's DNS
  and certificate checks are valid before its DNS is changed.
- Existing tenant custom domains are inventoried. Production provisioning now uses Railway's
  domain API; keep a production-scoped project token on Gateway before enabling new domains.

## Rollback

For application failures, restore the previous successful Railway Gateway and Web deployments,
Gateway first and then Web. Keep additive database migrations in place during an application
rollback. Restore Railway Postgres from a backup or point-in-time recovery only for confirmed data
corruption and only after separate approval. The retired Fly, Supabase, and Vercel resources no
longer exist and must not be referenced as rollback targets.
