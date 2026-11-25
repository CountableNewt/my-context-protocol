# Go Identity Service

The `cmd/identity` server implements these existing gateway routes against the
migrated SQLite/Turso schema:

- `GET /auth/github`
- `GET /auth/cli/github`
- `GET /auth/github/callback`
- `GET /auth/confirm`
- `GET /auth/me`
- `POST /auth/logout`
- `GET /health`

ATProto is enabled explicitly with `ATPROTO_ENABLED`, `ATPROTO_CLIENT_ID`,
`ATPROTO_CALLBACK_URL`, `ATPROTO_OAUTH_PRIVATE_KEY` (P-256 multibase), and
`ATPROTO_OAUTH_KEY_ID`. `ATPROTO_RECORD_COLLECTIONS` lists exact collection NSIDs;
`ATPROTO_SERVICE_PERMISSIONS` is JSON containing exact `{audience,method}` pairs,
where the audience is a DID service reference such as
`did:web:mycontextprotocol.dev#mycontextprotocol`. Publish client metadata at the
configured client ID; callback and metadata must share an HTTPS origin.

Enabled routes: `GET /auth/atproto/status`, `GET /auth/atproto`,
`GET /auth/cli/atproto`, `GET /auth/atproto/callback`,
`GET /auth/atproto/client-metadata.json`, `POST /auth/atproto/link`, and
`POST /auth/github/link`. Login accepts `handle` and `return_to` query values;
link requests accept JSON `{handle,return_to}` (GitHub omits `handle`) and return
`{authorization_url}`. Link requests require the existing session and trusted
Origin; callbacks require that same account plus one-use browser-bound state.
Different accounts are never merged. New ATProto accounts require the explicit
nullable-GitHub-ID migration and additive `ATProtoMigrationSQL` first.

`/auth/me` reports `github_connected`, `atproto_enabled`, and linked
`atproto_did` / `atproto_handle` without exposing OAuth session material. GitHub
bootstrap admin allowlists apply only to proven GitHub identities.

Session cookies remain `vapor-session`, with opaque 64-character keys and JSON
payloads using `accountId`. Cookies are host-only, HTTP-only, SameSite=Lax, and
secure for HTTPS frontend origins. Session establishment rotates keys; updates
never insert rows. Handoff consumption uses one atomic `DELETE ... RETURNING`
with the expiration predicate. Account IDs retain the uppercase UUID spelling
used by Fluent's SQLite storage. Timestamps use Unix seconds.

GitHub OAuth state retains Swift's `gh_oauth_v1` JSON/HMAC-SHA256 wire format,
using the configured 32-byte `ENCRYPTION_KEY`. GitHub credentials are encrypted
with the same AES-GCM serialization. State lives for ten minutes, one-time
handoffs for five minutes. Browser `return_to` remains frontend-only, while
verified callbacks additionally allow explicitly configured MCP resume origins
and the configured callback's API origin. CLI loopback URLs retain their strict
canonical numeric-loopback/state grammar.

Required environment:

- `TURSO_DATABASE_URL`, `TURSO_AUTH_TOKEN`
- `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`
- `GITHUB_OAUTH_REDIRECT_URI` (or `WEBHOOK_BASE_URL` for the standard callback)
- `ENCRYPTION_KEY`
- `FRONTEND_URL` or `CORS_ORIGIN`

Existing optional settings: `APP_ENV`, `GITHUB_APP_SLUG`, `WEBHOOK_BASE_URL`,
`MCP_OAUTH_RESUME_BASE_URLS`, `MCP_OAUTH_API_ORIGIN`,
`INTERNAL_ADMIN_GITHUB_LOGINS`, `INTERNAL_ADMIN_GITHUB_IDS`, `PORT`.

On Vercel, exact trusted `VERCEL_BRANCH_URL` and `VERCEL_URL` origins are added.
In preview environments the branch origin is canonical for confirmation redirects
and the native-client origin header. Arbitrary incoming hosts and unrelated
`*.vercel.app` domains are never allowlisted. This does not solve GitHub's OAuth
callback registration: each isolated preview still needs a GitHub-approved
callback configuration or a separately designed callback broker. A fixed
production callback cannot exchange tokens into an isolated preview database.

This service does **not** implement MCP OAuth start/resume, registration,
authorization, consent or token endpoints, GitHub App installation, or repository
listing. Those routes must continue using the legacy gateway until ported.
Existing MCP-start signed states can traverse this callback, provided both
services share the configured encryption key and database for that environment.
No database schema is created or migrated at server startup.

`go test -race ./identity ./cmd/identity` covers the browser/CLI handoff flow,
state tampering and expiration, return-target restrictions, preview confirmation,
CSRF logout, session revocation, and atomic SQL statements. Live GitHub and Turso
acceptance remains required before routing production authentication here.
