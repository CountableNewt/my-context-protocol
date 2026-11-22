# Go, Vercel, and Turso migration

Requested 2026-11-22. This is a migration record, not evidence of a completed cutover.

## Target

- One Vercel Hobby project, Git integration, `main` production, all other branches previews. No separate dev/prod projects.
- Next.js dashboard with Turbopack (already enabled for development and builds).
- Go HTTP services: identity, control API, MCP runtime, repository sync, and bounded maintenance requests.
- Turso Developer for now: owner upgraded on 2026-11-22 and explicitly chose to remain there. Production and preview credentials stay isolated. The original Free-tier target is deferred; Vercel remains Hobby.
- Full access for every account: no Pro tier, payment gates, project-count restrictions, webhook restrictions, or subscription reconciliation.
- Stop Railway compute only after verified data migration, production acceptance, and a restore rehearsal. Preserve backups through cutover.

## Platform constraints verified against owning documentation

Vercel Services and Go runtime are currently beta and available on all plans. Services ship together in one atomic deployment. Native Go callers cannot consume service bindings; let Next.js call Go over generated bindings, and let Go services share persistence rather than require binding-based RPC. Any necessary Go-to-Go binding calls need a supported runtime/container strategy first.

Bindings are runtime-only: do not bake them into `next.config.ts` rewrites. Browser requests stay same-origin. Preserve Next.js auth handlers that forward cookies and one-time OAuth redirects; ensure they call the identity service, never recurse through the public route.

Turso Free currently includes 100 databases, 5 GB storage, 500 million monthly rows read, and 10 million monthly rows written. Preview retention and cleanup must keep the account below those shared limits. Vercel Hobby is for personal/non-commercial usage; the owner confirmed that usage on 2026-11-22. Vercel paid plans and add-ons remain outside the authorized scope; the owner later selected Turso Developer as recorded below.

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

Those Go contract areas are now implemented and tested; the initial foundation limitation above is historical. Remaining operational acceptance includes real hosted repository sync/App delivery, tenant OAuth/domain routing, final data import and production cutover.

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

### Additional acceptance evidence

- Draft review: https://github.com/CountableNewt/my-context-protocol/pull/82. Feature branch is current with main. Automatic Git previews are working; stable branch alias is https://my-context-protocol-git-feature-mcp-cff674-sam-clemente-s-team.vercel.app. The separately tested migration callback alias remains https://my-context-protocol-migration-preview.vercel.app until its callback configuration is deliberately changed.
- Chrome completed GitHub sign-in and loaded the Go-backed dashboard, then created and persisted two acceptance projects as an ordinary account without a payment gate. Repository/App and tenant MCP acceptance are still pending.
- Actual development login uses GitHub App `mycontextprotocol-dev` (App ID 3137793), not the similarly named older OAuth App. The older OAuth App callback was restored; only the active App callback was moved to the migration preview. Homepage, wildcard and webhook settings were retained. Installation callback handling now supports OAuth-on-install with verified one-use intents, GitHub user identity and installation ownership.
- Read-only production schema comparison matched all 38 baseline objects exactly: no missing, changed or extra objects. Account aggregates showed zero stored subscription IDs and three null subscription statuses; no Stripe subscriptions were canceled.
- Latest frontend verification: 219 tests, typecheck and lint pass. Latest complete Go formatting/race/vet/build checks pass, including callback, legacy webhook and snapshot importer tests. Existing SQLite migration suite: 17 pass, one optional Postgres fixture skipped.
- The approved temporary preview token file was deleted after remote rehearsal. The accidentally CLI-created automation bypass was revoked. Preview authentication remains enabled.
- GitHub reported 12 existing default-branch dependency alerts (2 high, 10 moderate) during push; these are outside this migration's implementation and remain visible in repository security alerts.

The owner subsequently approved the private production snapshot and development App migration; both are complete as recorded below. Production import, DNS changes and Railway retirement remain unperformed.

## Latest verified state

- Owner chose Turso Developer for now after the account-wide sync allowance warning. This migration uses stateless SQL and the MCP preview reports zero embedded-sync bytes. Investigation of other projects' cumulative sync usage was deferred at the owner's request. No further plan change or overage setting was enabled by this migration.
- Created the approved empty classic SQLite database `my-context-protocol-production`, default `aws-us-east-2`, URL `libsql://my-context-protocol-production-countablenewt.aws-us-east-2.turso.io`. A database-scoped read/write token with no expiry is in Vercel Production and the specifically approved temporary mode-600 file `/private/tmp/mcp-production-rehearsal/production.env`; delete that credential file after migration. Read-only probe confirmed zero application tables. No production data imported.
- Prepared only Vercel Production: preserved the existing Railway encryption and OAuth client credentials, generated separate internal/cron credentials, retained existing public callback origins and MCP domain/path. Existing production accounts and repo connections do not use GitHub App installation IDs; the existing production OAuth flow is retained.
- Approved development App key is stored only as a branch Preview Secret. Its generated local download was removed. App client aliases/slug/shared callback and webhook secret are branch-scoped. Development App webhook URL points to the migration alias; external signed delivery acceptance remains pending.
- Private consistent source backup: 8,286,208 bytes; integrity `ok`, zero FK violations. SHA-256 `1f2098cf41cf3111e60555872504e10989bbd2f907ca0848ea7306f1e6c2826c`. The backup and typed artifact are mode 600 under `/private/tmp/mcp-production-rehearsal` (mode 700).
- Local restore verified all 30 tables and 16,299 typed rows. Go schema additions preserved every source count/hash. Both imported projects passed MCP initialize, tools/list and catalog calls: 9 ready skills, 9 capabilities, 8 listed tools, 6 MCP calls. An additional project edit survived typed remote-export/offline-restore rollback. Source backup/artifact hashes remained unchanged; the private rehearsal made zero external requests. Metadata-only reports stay in the private directory.
- Actual hosted CLI acceptance passed through ephemeral same-project OIDC authentication: Chrome browser handoff, two visible preview projects, MCP initialize, tools/list and catalog. The temporary MCP key and CLI session were revoked; no permanent platform bypass was used.
- Full GitHub CI passed again in https://github.com/CountableNewt/my-context-protocol/actions/runs/45062873802 (Go, Swift CLI Linux, macOS full tests/build). Latest added fixture/rollback/domain changes also passed relevant race tests, vet and Go builds locally.
- Preserved root REST paths on existing API hostnames in `vercel.json`, alongside the unified `/api` route, so saved legacy CLI profiles remain compatible at cutover.
- Added renewing short-lived Vercel integration authentication for custom domains. Owner approved an unlisted integration with project/domain read-write and only this project selected. The draft is prepared in the Integrations Console; no integration has yet been created or agreement accepted. Browser logo upload stalled and native picker access was denied. Finish the existing app-icon upload and obtain action-time approval for the required Marketplace Agreement before creation/install.

Remaining before retirement: finish provider integration/install, hosted repository sync and signed-hook acceptance, actual tenant MCP OAuth/domain tests, final write freeze and fresh verified import, production DNS/deployment acceptance and rollback verification, then stop Railway compute while retaining recovery artifacts. The current private rehearsal snapshot is not a final cutover snapshot.

### Hosted repository acceptance: App webhook correction

Chrome ordinary-account acceptance selected `CountableNewt/my-context-protocol`, branch `feature/mcp-22-sqlite-primary`, in the preview acceptance project. The App installation callback returned successfully, but connecting then failed with GitHub HTTP 403 during repository-hook creation. The Go port was asking an installation token to create a second repository webhook even though this App already has a signed, configured shared webhook.

The corrected connection uses the existing App webhook for installed repositories and stores no per-repository hook secret/ID. OAuth-only connections retain their signed repository hooks. Before saving an App connection, read-only registration checks require an existing `push` subscription, JSON, TLS verification and an exact configured `/webhooks/github` or `/webhooks/github-app` target; the local shared signature secret must exist. No permissions, subscriptions or webhook settings are expanded automatically. Existing shared-route dispatch preserves push, ping and App lifecycle signatures. Regression tests exercise both connection modes and reject mismatched/missing App registration settings. The corrected preview subsequently connected the repository successfully, verifying the installed App registration. No repository webhook or additional GitHub permission was needed.

GitHub documents the App-wide webhook and OAuth distinction in [Differences between GitHub Apps and OAuth apps](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/differences-between-github-apps-and-oauth-apps), with read-only registration endpoints in [Apps](https://docs.github.com/en/rest/apps/apps) and [App webhooks](https://docs.github.com/en/rest/apps/webhooks).

### Domain integration and production preparation

- Created the approved unlisted `mycontextprotocol-domains` integration after explicit Marketplace Agreement acceptance. Required logo, public sign-in screenshot, existing MIT license and integration-specific privacy notice were supplied. Installation `icfg_6BdycmI6fX5W2hom3u7QVJKv` passed exact team, mandatory configuration-read/project-write/domain-write scopes and specific-project-only checks. Three credentials are Secret variables only in Vercel Production; no plaintext credential file was created. Fixed `VERCEL_TEAM_ID` is configured. The one-time callback helper exited successfully.
- Production preparation build `dpl_DRSuVQM3h8xtUbASFFrqpMdFgj51` passed with `--skip-domain`; no custom domain or existing traffic moved. A later build includes provider credentials and the App webhook correction. Production Turso remains empty.
- Actual App repository connection initially failed because the port tried to create an extra repository webhook using an installation token. App connections now validate and reuse the existing signed App hook; OAuth connections retain individual signed hooks. Targeted race/vet/build checks pass, and hosted connection succeeded after the fix. Hosted sync acceptance is still underway.
- Existing Marque zone was inspected: production traffic and Railway ACME records are present, alongside mail/AT Protocol/verification records. No DNS records have changed. Wildcard TLS can use Vercel's documented external-DNS ACME challenge delegation; whole-zone nameserver migration is unnecessary.

### Hosted archive acceptance: GitHub PAX metadata

The next hosted sync attempt failed before compilation with `sync_failed`. A credential-free read of the same public GitHub tarball reproduced `unsafe repository archive entry`: GitHub begins tarballs with a global PAX header carrying the resolved commit. The extractor now accepts that bounded metadata without writing it or applying its paths, links, modes or sizes; all file/link/traversal and total stream/count limits remain enforced. The resolved full commit is retained from the standard comment metadata, avoiding a second mutable branch resolution. Tests cover GitHub-shaped archives, oversized metadata, and traversal/symlink/hardlink rejection following the header. The dashboard also recognizes App installation webhook connections without a per-repository hook ID. The corrected archive build still requires hosted sync/compiler/MCP rerun.
