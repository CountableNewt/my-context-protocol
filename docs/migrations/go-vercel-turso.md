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
- Linear: plugin discovered but not connected; issue updates pending connection.
- Vercel: connected account has no existing project matching `context` at initial inspection.
- Railway retirement: pending all acceptance above.
