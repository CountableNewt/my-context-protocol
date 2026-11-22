## Learned User Preferences

- When committing on this repo, keep unrelated local changes out of the commit and honor an explicitly provided staged-file list as authoritative.
- Use Chrome for browser validation unless the user specifically requests otherwise.

## Learned Workspace Facts

- This is a Bun/Turbo monorepo: `apps/web` is the Next.js 16 dashboard, `services/go` is the active backend, `apps/context` is the active Swift CLI, and `packages/*` contains shared contracts/types.
- Active Go HTTP services are `cmd/identity`, `cmd/control`, `cmd/mcp` and `cmd/sync`. Bounded maintenance runs through control; perpetual background loops are not the hosted architecture.
- Production runs in one Vercel Hobby project using root `vercel.json`. `main` is the Production branch; feature branches use branch-scoped Previews, not separate Dev/Prod projects. Next.js uses Turbopack and server-only runtime service bindings for same-origin `/api` forwarding.
- Persistence uses stateless Turso HTTP queries. The owner chose Turso Developer; the original Free-tier target is superseded. Production and preview databases/credentials remain isolated. Never upload production data to an ordinary preview.
- Tenant MCP uses `/mcp` on mapped tenant or verified custom hosts. Tenant OAuth requires its actual host, PKCE and one-use consent. A generic Vercel deployment URL does not establish tenant-host acceptance.
- Explicit schema and snapshot tooling lives in `services/go/cmd/migrate`, `services/go/cmd/import` and `services/go/snapshotimport`. Services must not run schema migrations at startup. Preserve encryption/signing secrets during transfers and keep strict typed-row verification.
- Railway services are deleted; the owner scheduled the whole project for deletion within 48 hours on 2026-11-22. Retired Swift gateway and Railway tooling are removed from the working tree. Do not reintroduce stale deployments. Current recovery uses the Go snapshot tools and requires paused writers and verified current data before changing traffic.
- GitHub Actions CI is defined by `.github/workflows/ci.yml` and `scripts/ci.sh`: Go format/vet/race/build, frontend checks, Swift CLI format/test/build and current Go snapshot/migration tests. Linux also checks the Swift CLI. Preserve the required **Test and build** check name. Depot workflows are legacy and must not be reintroduced.
- Every account has full feature access. No Pro tier, payment gates or payment-based project/webhook limits should be reintroduced.
- Migration evidence is maintained in `docs/migrations/go-vercel-turso.md`; Railway retirement inventory is in `docs/runbooks/railway-compute-retirement-2026-11-22.md`.
- The frontend was merged into this repository by git subtree from the archived `Stygian-Tech/my-context-protocol-frontend` history.
- The internal MCP agent guide is maintained outside this open-source repo. Do not add Notion URLs or re-add an MCP agent guide file without an explicit request.
