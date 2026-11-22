# Go, Vercel, and Turso migration

Requested 2026-11-22. This is a migration record, not evidence of a completed cutover.

## Target

- One Vercel Hobby project, Git integration, `main` production, all other branches previews. No separate dev/prod projects.
- Next.js dashboard with Turbopack (already enabled for development and builds).
- Go HTTP services: identity, control API, MCP runtime, repository sync, and bounded maintenance requests.
- Turso Free: production database and isolated preview database credentials; preview builds never receive production credentials.
- Full access for every account: no Pro tier, payment gates, project-count restrictions, webhook restrictions, or subscription reconciliation.
- Stop Railway compute only after verified data migration, production acceptance, and a restore rehearsal. Preserve backups through cutover.

## Platform constraints verified against owning documentation

Vercel Services and Go runtime are currently beta and available on all plans. Services ship together in one atomic deployment. Native Go callers cannot consume service bindings; let Next.js call Go over generated bindings, and let Go services share persistence rather than require binding-based RPC. Any necessary Go-to-Go binding calls need a supported runtime/container strategy first.

Bindings are runtime-only: do not bake them into `next.config.ts` rewrites. Browser requests stay same-origin. Preserve Next.js auth handlers that forward cookies and one-time OAuth redirects; ensure they call the identity service, never recurse through the public route.

Turso Free currently includes 100 databases, 5 GB storage, 500 million monthly rows read, and 10 million monthly rows written. Preview retention and cleanup must keep the account below those shared limits. Vercel Hobby is for personal/non-commercial usage; the owner confirmed that usage on 2026-11-22. No paid plan or paid add-on is authorized.

Sources: [Services](https://vercel.com/docs/services), [Go runtime](https://vercel.com/docs/functions/runtimes/go), [bindings](https://vercel.com/docs/services/bindings), [Hobby](https://vercel.com/docs/plans/hobby), [Turso pricing](https://turso.tech/pricing).

## Migration sequence and acceptance

1. Remove payment restrictions from current backend/frontend. Keep historic database columns compatible while eliminating billing entry points and schedulers. Restore projects previously suspended by a downgrade. Test multiple projects, hooks, custom domains, and analytics for ordinary accounts.
2. Add Go persistence/security foundations. Verify AES-256-GCM nonce+ciphertext+tag serialization, SHA-256 credential hashes, and session payloads against Swift compatibility fixtures. Never rotate encryption keys during data transfer.
3. Port identity and control contracts, including owner checks, admin checks, CSRF, opaque sessions, single-use handoffs, MCP OAuth PKCE and atomic token consumption.
4. Port MCP and sync, including compiler/runtime policy, capability visibility, tenant host binding, bounded package reads, signed GitHub hooks, and transactional release activation. Compare existing Swift behavior with Go against the same fixtures.
5. Replace process-local catalog revisions/subscriptions/rate limits with shared durable state. Replace perpetual lifecycle loops with bounded, idempotent maintenance. Confirm actual Hobby scheduler/function limits before selecting cadence. Bound SSE connections and require reconnect/resume.
6. Provision the single Vercel project and isolated Turso preview database. Verify branch environment selection, protected preview OAuth callbacks, cookie forwarding, custom-domain provisioning and actual budget settings.
7. Rehearse migration from a consistent SQLite backup: integrity checks, schema and row counts, timestamps, IDs, indexes, encrypted tokens, sessions and representative catalogs. Do not upload private production data to ordinary previews.
8. Run complete tests and builds, Chrome authenticated dashboard/CLI/OAuth checks, and real MCP acceptance on preview. Preserve Swift CLI checks independently of replacing the Swift backend.
9. Freeze writes, take final backup, import and verify production, switch domains, run signed-in acceptance, and verify rollback including writes made after cutover.
10. Stop Railway services and record exact resources retained/removed. Do not describe the migration as complete before this evidence exists.

## Known porting hazards

- `McpCatalogNotifications` and resource subscriptions are process-local.
- Repository sync shells out to tar and can fetch 150 MiB archives. Go extraction must enforce paths, symlinks, file counts, expanded bytes and deadline limits.
- Webhook sync currently completes inline; retries need durable idempotency/job state.
- Startup currently runs all Fluent migrations; serverless functions must not race automatic schema changes.
- Tenant custom-domain management currently calls Railway APIs.
- Session update must not revive a session concurrently deleted by logout.

## Access and progress

- Starting checkout: clean `feature/mcp-22-sqlite-primary`; retained per repository instructions.
- Custom MyContextProtocol skills endpoint: request returned HTTP 401; unavailable until authentication is repaired.
- Linear: connection declined by owner; migration evidence is tracked in this document.
- Vercel: connected account has no existing project matching `context` at initial inspection.
- Railway retirement: pending all acceptance above.

## 2026-11-22 implementation waypoint

- Payment gates removed from Swift backend, dashboard and Swift CLI. Legacy billing columns remain compatible; no external subscriptions were canceled by these changes.
- Swift gateway: 235 tests pass serially, with warnings-as-errors compilation.
- Frontend: 206 tests pass, typecheck and lint pass (two existing navigation warnings); Next.js upgraded to 16.4.6 and production Turbopack build passes.
- Swift CLI: format check, warnings-as-errors build, 8 Swift tests, 15 HTTP/CLI tests and release build pass with the installed Xcode beta toolchain.
- Existing SQLite migration tests: 17 pass, one external Postgres fixture test skipped.
- Go module now has token/session/Turso primitives, GitHub browser/CLI identity routes, and bounded repository archive fetch/extraction. These are not full gateway parity.
- Runtime Next API forwarding supports identity/control/sync bindings with an explicit route allowlist, same-origin cookies, uncached account data, and fail-closed missing preview bindings. `VERCEL_SERVICE_MODE=1` must only be enabled when those services are deployed and ready.
- Created empty `mcp-migration-preview` on the existing Turso Free account after explicit owner approval, default `aws-us-east-2` group. No production data imported. URL: `libsql://mcp-migration-preview-countablenewt.aws-us-east-2.turso.io`.
- Account dashboard displayed 82% shared embedded-sync quota used. Use stateless remote HTTP queries for this migration; do not create embedded replicas or run bulk sync rehearsals against that remaining quota.
- A protected Vercel Preview deployment built successfully; production domain cutover has not occurred. Railway remains live.

### Remaining identity acceptance

A single production GitHub OAuth callback cannot create a handoff in production storage and have it redeemed from an isolated preview database. Before hosted branch login, implement and validate a deliberate callback broker or use separately configured preview OAuth credentials/callbacks. Origin allowlisting alone does not solve this. Route `/auth/github/callback` explicitly to identity; the dashboard API proxy alone only owns `/api/*`.

Remaining backend port includes MCP OAuth/consent/token registration, GitHub App install and repository listing, control API, compiler/runtime, sync publication and webhooks, shared revision/subscription/rate-limit state, maintenance scheduling, custom domains, controlled schema migration and data verification. The current Go identity slice must not receive unsupported routes.

Go verification: race-enabled tests, `go vet`, and `go build ./...` pass for the implemented packages. Final frontend regression also preserves host-only sessions on deployment-specific preview URLs when backend defaults use a branch alias.

## 2026-11-22 remote rehearsal and Go implementation

Implemented identity, control, MCP runtime/OAuth, compiler, repository sync, GitHub App operations, custom-domain provider, controlled migrations, and bounded daily maintenance. Go uses stateless Turso HTTP transactions with foreign keys enabled on each stream. Initial migration registry creation and baseline adoption commit atomically; imported schemas require explicit verified adoption.

- Full Go race tests, vet, and command builds pass. Frontend: 218 tests, typecheck and lint pass (existing navigation warnings). Latest local Turbopack attempt is blocked by worker socket sandbox permissions; the hosted Next 16.4.6 Turbopack build succeeded.
- Created one Hobby project `my-context-protocol` (`prj_jC1eRUJ0CHZvHp7Eej3AUAS4yUq7`), linked to GitHub with `main` production. No separate development project.
- Explicit Preview deployment `dpl_FYeJ1wdxT8c4baZKyBBvz3xzrLNs` built all five services. Stable migration alias: https://my-context-protocol-migration-preview.vercel.app. Initial smoke found a root routing 404 and identity startup failure from missing GitHub credentials; validation remains in progress.
- Preview-only database token and encryption/internal/cron credentials were approved and provisioned for `feature/mcp-22-sqlite-primary`. No production credentials or data in this preview. Database token expires after 30 days.
- Actual Turso schema initialization and maintenance indexes applied. Synthetic remote rehearsal passed transactions/rollback, exact integers/blobs, encrypted token compatibility, session revocation, one-time handoffs, and foreign-key enforcement/integrity. Synthetic rows were removed.
- Owner approved moving development GitHub OAuth to the stable migration preview. Existing development client credentials copied directly Railway→branch Preview without local persistence; callback changed to the stable alias. Production OAuth remains unchanged. GitHub App install configuration and tenant OAuth host acceptance remain outstanding.
- Read-only Railway production inventory: SQLite 8,286,208 bytes, 30 tables, 3 accounts, 2 projects, 47 Fluent migrations. No production backup/import/cutover has yet been performed.
- Preserve the source encryption key during production migration. Production write freeze, consistent snapshot, restore rehearsal, row/schema comparison, authenticated dashboard/CLI/MCP/domain acceptance, and Railway shutdown are still required.

Vercel CLI initially classified a failed first deployment as Production; it never became live and no existing domain moved. Every subsequent deployment explicitly uses `--environment preview`. The authenticated curl helper unexpectedly created an automation bypass credential; revoke it after validation and retain preview authentication.
