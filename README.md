# MyContextProtocol

MyContextProtocol ingests `SKILL.md` files from Git repositories, compiles policy-aware MCP capabilities, and serves them through tenant endpoints with authentication and audit logs. Every account has full feature access; there is no paid tier or payment-based project limit.

## Architecture and layout

Production runs on **one Vercel Hobby project** with a Next.js 16 dashboard, Turbopack development/builds, and four Go HTTP services. Persistence uses stateless Turso HTTP queries on the owner's chosen **Turso Developer** plan.

```text
apps/context/                   Swift MyContextCLI (myctx)
apps/web/                       Next.js dashboard and same-origin API forwarding
packages/mycontext-api-contract/ API contract documentation
packages/mycontext-web-client/   Shared TypeScript contracts
services/go/                    Active Go backend
  cmd/identity/                 GitHub login, sessions and installation callbacks
  cmd/control/                  Projects, releases, analytics and bounded maintenance
  cmd/mcp/                      Tenant MCP, OAuth/PKCE and streaming notifications
  cmd/sync/                     Signed webhooks and bounded repository compilation
  cmd/migrate/                  Explicit schema migration/adoption
  cmd/import/                   Verified snapshot transfer and offline rollback
services/mcp-gateway/            Legacy Swift/Vapor recovery and reference source
scripts/                        Reproducible CI and operator tooling
docs/                           Architecture, migration evidence and runbooks
```

The dashboard forwards browser API requests through `/api` using server-only runtime service bindings. Tenant MCP uses `/mcp` on mapped tenant or verified custom hostnames. Schema changes are explicit operator commands; services do not migrate databases at startup.

## Local development

Install Bun workspace dependencies with `bun install --frozen-lockfile`. The Go services read exported environment variables; use [the service environment template](services/go/env.example) and [Go service guide](services/go/README.md) to configure an isolated development database, GitHub callbacks and local service origins.

For individual service processes, run these in separate terminals from `services/go`, after exporting the required environment:

```sh
PORT=8081 go run ./cmd/identity
PORT=8080 go run ./cmd/control
PORT=8082 go run ./cmd/mcp
PORT=8083 go run ./cmd/sync
```

Run `bun run dev` from `apps/web`. For local Go service routing, configure `VERCEL_SERVICE_MODE=1` with `IDENTITY_SERVICE_URL`, `CONTROL_SERVICE_URL` and `SYNC_SERVICE_URL` pointing to those local processes. Hosted deployments receive those bindings from `vercel.json`; do not copy binding URLs between deployments. Configure browser API requests to use the local dashboard's `/api` route.

Database initialization and legacy adoption are separate, explicit operations. Follow the [snapshot workflow](services/go/snapshotimport/README.md) when importing an existing database, and preserve the original encryption/signing secrets.

## MyContextCLI

The Swift CLI remains active and supports account, project, release, skill, runtime and administrator operations with JSON output. It requires Swift 6+ on macOS or Linux.

```sh
cd apps/context
make install
myctx auth login
myctx projects list
myctx commands
```

The default account API is `https://mycontextprotocol.dev/api`. Override it with `--url`, `MYCTX_URL` or a saved profile for self-hosting or a branch preview. A project's tenant MCP URL is separate from the account API. Installation produces `~/.local/bin/myctx`; `make build` produces `bin/myctx`.

See [the CLI guide](apps/context/README.md) for profiles, automation, payloads and the legacy `CONTEXT_*` configuration fallback.

## CI and deployment

[GitHub Actions](.github/workflows/ci.yml) runs the same active checks as:

```sh
bash scripts/ci.sh
```

Active CI covers Go formatting, vet, race tests and builds; frontend type checks, lint, tests and Turbopack build; Swift CLI formatting, tests and build; and SQLite migration tests. A separate Linux job validates the Swift CLI. The required GitHub check remains **Test and build**. Go SQL integration tests use Python 3; the deployed Go runtime does not require Python or a local SQLite database.

The single Vercel project uses `main` for Production and feature branches for Previews, with isolated branch-scoped preview credentials and data. There are no separate Dev and Prod Vercel projects. Do not place production credentials or private production snapshots in ordinary previews. The [migration record](docs/migrations/go-vercel-turso.md) documents the verified production import, Chrome/CLI/MCP acceptance, signed GitHub delivery and rollback rehearsal.

## Recovery and retired infrastructure

Railway Development and Production compute are off. Service definitions, volumes and backups remain for recovery; retained storage can still incur charges. See the [retirement inventory](docs/runbooks/railway-compute-retirement-2026-11-22.md).

The former Swift/Vapor gateway remains available for manual recovery checks:

```sh
bash scripts/check-legacy-swift-gateway.sh
```

Its external `mcp-server-kit` dependency applies to the legacy gateway. See [legacy gateway recovery](docs/migrations/legacy-swift-gateway.md) for toolchain and dependency requirements. It is not part of default active-backend CI.

After Go has accepted writes, a stale Railway database is not a current rollback target. Pause and drain all destination writers, export the post-write data, restore and verify a new SQLite database, then explicitly restore the stopped gateway and switch routing. The [snapshot and rollback workflow](services/go/snapshotimport/README.md) preserves typed rows and Fluent history; it does not deploy or change traffic automatically.

## History

The repository retains the original backend history. The frontend was merged through `git subtree` from the archived `Stygian-Tech/my-context-protocol-frontend` repository. Earlier Fly/Supabase deployments and Railway deployment configuration are historical, not active deployment instructions.

## License

[MIT](LICENSE).
