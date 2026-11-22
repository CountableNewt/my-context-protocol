# Go, Vercel, and Turso migration

Requested and cut over on 2026-11-22. Production now runs on the single Vercel Hobby project and Turso Developer; all Railway compute is retired with storage and backups retained. The dated preparation entries below are historical; the final evidence section records the completed acceptance and rollback checks.

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

The owner subsequently approved the private production snapshot and development App migration; both are complete as recorded below. At that preparation checkpoint, production import, DNS changes and Railway retirement had not yet occurred; all are completed in the final evidence below.

## Pre-cutover preparation record

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

The preparation gates were provider integration/install, hosted sync and signed hooks, tenant OAuth, final frozen import, production acceptance and rollback, followed by Railway retirement. The early private rehearsal snapshot was not the final cutover snapshot. These gates were subsequently completed as recorded below.

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

The next hosted sync attempt failed before compilation with `sync_failed`. A credential-free read of the same public GitHub tarball reproduced `unsafe repository archive entry`: GitHub begins tarballs with a global PAX header carrying the resolved commit. The extractor now accepts that bounded metadata without writing it or applying its paths, links, modes or sizes; all file/link/traversal and total stream/count limits remain enforced. The resolved full commit is retained from the standard comment metadata, avoiding a second mutable branch resolution. Tests cover GitHub-shaped archives, oversized metadata, and traversal/symlink/hardlink rejection following the header. The dashboard also recognizes App installation webhook connections without a per-repository hook ID. The corrected archive build passed the hosted sync/compiler/MCP rerun below.

### Hosted repository/compiler/MCP acceptance passed

Preview `puiohaqhg` (`dpl_3m2XjieFV5PkQRC3cBv5R7ZnPHHE`, commit `981e14d`) built successfully, and only the existing migration preview alias was repointed. An ordinary account connected `CountableNewt/my-context-protocol`, branch `feature/mcp-22-sqlite-primary`, then fetched and compiled its public `services/go/compiler/testdata/hosted-smoke/SKILL.md` fixture into one ready skill. The release retained the fetched full commit and was activated. Normal Chrome OAuth authenticated a temporary CLI session; same-project short-lived OIDC authenticated protected preview requests. MCP initialize, tools/list and get_skill all succeeded, and get_skill returned marker `MCP_HOSTED_SMOKE_V1`. The temporary MCP key and CLI session were revoked after acceptance. No permanent bypass or production change was used.

Chrome independently displayed commit `981e14d` as ready/active and the repository branch plus “Configured through GitHub App” automatic-sync status. Earlier failed releases remain as diagnostic history. This verifies manual hosted repository sync and retrieval; actual externally signed GitHub push delivery acceptance remains separate, as do tenant-host DNS/OAuth and production cutover.

### Tenant OAuth consent form correction

Testing-tenant TLS and discovery passed, as did dynamic public-client registration, S256 advertisement and cross-project client rejection. The normal Chrome GitHub broker reached tenant consent for the temporary local acceptance client. After the owner explicitly approved that exact client/project/`mcp:invoke` grant, the native form POST failed with `Origin is not allowed` before issuing any code/token. The global `Referrer-Policy: no-referrer` makes non-CORS browser form submissions serialize Origin as `null` under the [Fetch Standard](https://fetch.spec.whatwg.org/#append-a-request-origin-header).

The consent HTML alone now uses `Referrer-Policy: same-origin`, preserving the native same-origin POST identity while suppressing cross-origin referrers. Other OAuth responses retain no-referrer. Origin allowlists and one-use consent tokens are unchanged: regression tests reject opaque/foreign form origins without consuming consent, then accept the rightful tenant Origin. Relevant OAuth/identity/MCP race tests, vet and builds pass. The timed-out temporary CLI session was revoked; the exact registered client was reused for the owner-approved grant on the corrected preview, then removed with its pending/code/token rows as verified below.


### External signed GitHub delivery acceptance passed

The owner explicitly approved Deployment Protection exceptions for exactly five domains: `my-context-protocol-migration-preview.vercel.app`, `testing.mycontextprotocol.dev`, `api.testing.mycontextprotocol.dev`, `mcp.testing.mycontextprotocol.dev`, and `*.mcp.testing.mycontextprotocol.dev`. Those exceptions were applied in the live Hobby project UI. Generated deployment URLs remain protected, and no bypass credential was created. Vercel's [September 9, 2026 announcement](https://vercel.com/changelog/protect-production-deployments-for-free-on-every-plan) confirms domain exceptions are available free on every plan; the current Hobby UI independently confirmed availability. Earlier assumptions that this required a paid plan are superseded.

Chrome opened the existing development GitHub App's delivery history and used GitHub's own Redeliver control for push delivery `314ece2a-e6b7-11f1-9de3-f074599954a9`. Its original attempt received HTTP 401 from deployment protection. The redelivery at 2026-11-22 21:17:07 UTC reached the public migration alias directly and received HTTP 200 from Vercel with `status: processed` and one ready job:

- Project: `C98D7F05-CB52-4EDB-BDA6-F7870A378784`.
- Job: `F4B84A48-F75B-48E1-9E5C-C2BCE45AD939`.
- Release: `9AF9F644-3F92-4841-A4E4-DF25145C0DF7`.

A separate read-only Turso UI query confirmed that exact job exists with `delivery_key = github:314ece2a-e6b7-11f1-9de3-f074599954a9`, the same release ID, `status = ready`, and `error_code IS NULL`. This verifies an actual provider-signed external push through ingress, signature validation, repository selection, sync and durable release publication. GitHub supplied its original payload/signature directly: no reconstructed request, extracted signing secret, OIDC replay or permanent bypass was used. Another actual push returned HTTP 200 with an empty jobs list, which only demonstrates accepted ingress without a matching target and is not the sync acceptance evidence.

This completed external signed-hook acceptance before the final production import, domain cutover and Railway retirement recorded below.

Consent navigation also permits only the validated, registered callback origin in `form-action`, alongside `'self'`, because Chrome applies that directive to the successful 303 form redirect. It retains default-src none and framing/base restrictions; redirect hostnames containing CSP delimiters or wildcards are rejected. Tests cover the exact loopback callback allowance, absence of wildcard policy, unsafe redirect metadata rejection, and the unchanged form-Origin checks. This follows the [CSP form-action pre-navigation check](https://www.w3.org/TR/CSP/#directive-form-action).

### Tenant OAuth acceptance and cleanup passed

Corrected preview `95jx1blvr` (`dpl_GsbT8MU4DSoTtyr8GF3YxfAaAuWF`) served both testing tenants and the migration broker alias. The owner explicitly authorized the same temporary local public OAuth client for `mcp:invoke` on Migration acceptance one. Normal Chrome GitHub login reached consent, Approve returned to the originally registered loopback callback, and the S256 code exchange succeeded. MCP initialize succeeded with that token on its tenant. Cross-project client authorization and token exchange, wrong PKCE, wrong resource, code replay and cross-project bearer use were all rejected.

The exact temporary public client and its three pending/code/access-token child sets were removed through the authenticated preview SQL UI. A separate query confirmed zero matching clients, and the still-running acceptance helper confirmed that MCP rejected the removed token before exiting successfully. The temporary CLI session was revoked; Chrome independently showed the broker session signed out at the login page. No replacement client, persistent bypass or production credential was created for this rerun. This completes the tenant OAuth acceptance gate before production freeze. Full GitHub CI passed for the consent-fix code.


## Final production cutover and recovery evidence

Production is live on Vercel/Turso. The production dashboard, API, MCP base, tenant wildcard and existing verified custom-domain routing were moved after certificates and Vercel domain configuration were ready. Marque remains authoritative for the apex zones; app-specific ACME challenge NS delegation supplies wildcard certificate renewal. Mail, AT Protocol and unrelated verification records were preserved. An initial custom-domain HTTP 502 was traced to a cached Railway DNS answer: a TLS-verified request to the assigned Vercel address returned the expected HTTP 401 and MCP metadata challenge, and subsequent ordinary-host acceptance passed.

The final frozen source snapshot was imported and strictly verified across **30 original tables and 16,299 rows**, both before and after additive Go migrations. Typed multiset hashes matched the source exactly, including real-number bit patterns. Twelve timestamp cells initially differed by one ULP through SQL transport; the guarded staging repair restored their source values before strict verification passed. This was an exact import gate, not a relaxed float comparison. The private reports are `final-float-repair-report.json`, `final-verify-after-report.json` and `final-migration-report.json`; they contain no credentials in this repository. Original snapshot ID: `5bb7868a40fcc6151cf51591530b2f5811ca991109883e26d5c7517b50344c01`.

Production acceptance used normal Chrome GitHub login and the public dashboard API with an isolated temporary CLI configuration. The authenticated account could read one existing project and 56 GitHub repositories. Its account-returned public MCP URL passed an unauthenticated HTTP 401/Vercel/resource-metadata preflight, then temporary-key initialize, tools/list, catalog and get_skill requests succeeded. One existing custom-domain status read also succeeded. The temporary key was revoked, the CLI session logged out and the private configuration removed. No production project/release was created or changed for acceptance, and no new OAuth client or consent grant was registered. Chrome had no authenticated pre-cutover cookie, so imported browser-session continuity was not verified. Fresh login updates the account GitHub token; repository listing proves the live encrypted-token/provider path, not independent preservation of untouched imported account ciphertext. Exact import checks and prior compatibility fixtures provide separate data/serialization evidence.

A fresh **post-Go-write** destination export was restored into an offline SQLite database, then exported again and compared exactly: **30 original tables, 16,313 rows**, identical typed table hashes and snapshot ID `32f5707a161c427bb3ed1c3c162dc5dd44f7ed4c7742562bfc7462cf0dd8f5f0`. Private evidence is `post-go-roundtrip-report.json` and `post-go-local-restore-report.json`. The exporter uses setting-free source reads so the frozen source is not mutated while capturing a snapshot. This proves reverse transfer of the tested post-cutover dataset; it does not authorize restarting a stale Railway source after later writes. A future rollback must first pause destination writers and repeat the verified reverse transfer.

At **2026-11-22 22:18 UTC**, all six Railway Development/Production Web, Gateway and Postgres deployments were terminally removed, with stopped/exited instances and zero active or queued compute. Service definitions, domains, variables, all four environment-specific volume instances and **25 existing backups** remain. GitHub auto-deploys and Postgres image auto-updates are disabled. Storage can still incur charges and backups retain their expiry policies. See [exact retirement inventory and rollback precautions](../runbooks/railway-compute-retirement-2026-11-22.md).

After Railway removal, public checks returned dashboard HTTP 200, API health HTTP 200, authenticated-project routes HTTP 401 on both `/api/projects` and legacy `/projects`, custom MCP metadata HTTP 200 and MCP HTTP 401, all served by Vercel. The approved temporary Railway SSH registration was removed and independently confirmed absent; the temporary production credential file was deleted and confirmed absent. Private backup/recovery artifacts remain protected.

The Swift gateway source remains legacy recovery/reference code. Default CI retains Go format/vet/race/build, frontend checks, Swift CLI format/test/build and SQLite migration tests; the former gateway suite is preserved as an explicit [manual recovery command](legacy-swift-gateway.md). The required GitHub check name remains Test and build. Final active CI must pass on the PR head before merge. The Vercel project remains Hobby; the owner's explicit Turso Developer choice supersedes the initial Free-tier target.
