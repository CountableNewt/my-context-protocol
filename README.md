# MyContextProtocol

Stygian-style monorepo for MyContextProtocol: a hosted MCP gateway that ingests `SKILL.md` files from Git repositories, compiles them into policy-aware MCP capabilities, and exposes them through stable audited endpoints.

## Layout

```text
.
├── apps/
│   ├── context/                     # MyContextCLI (myctx) for agents and account management
│   └── web/                         # Next.js 16 / Bun dashboard
├── packages/
│   ├── mycontext-api-contract/      # Backend API contract docs
│   └── mycontext-web-client/        # Shared frontend/API-facing TypeScript types
├── services/
│   └── mcp-gateway/                 # Swift / Vapor MCP gateway
├── docs/
│   ├── architecture/
│   └── test-plans/
├── scripts/                         # Local CI and deploy entrypoints
├── package.json                     # Bun workspaces + Turbo
└── turbo.json
```

The backend also consumes the external Swift package `mcp-server-kit` for reusable MCP protocol primitives. During local two-repo development this is wired as a sibling checkout; publish and pin that package before relying on GitHub Actions or Railway remote builds.

## Local Development

```bash
# Install all Bun workspace dependencies
bun install

# Backend (port 8080 by default)
cd services/mcp-gateway
cp .env.example .env
swift run App

# Frontend (port 3000 by default)
cd ../../apps/web
cp .env.example .env
bun dev
```

Set the frontend `NEXT_PUBLIC_API_URL` to the backend origin, usually `http://localhost:8080`.

## MyContextCLI

MyContextCLI (`myctx`), written in Swift, exposes the complete account, project, release, skill, runtime, billing,
and administrator API with JSON output for agents. Requires Swift 6+ on macOS or Linux.

```bash
cd apps/context
make install
myctx auth login
myctx projects list
myctx commands
```

The CLI defaults to the hosted account API at `https://api.mycontextprotocol.dev`.
For self-hosting or an explicit Development environment, override it with `--url`,
`MYCTX_URL`, or a saved profile URL. Precedence is flags, environment, profile,
then hosted default; existing profiles retain their explicit URL. A personal
tenant MCP endpoint is separate from the account API.

Install produces `~/.local/bin/myctx`; `make build` produces `bin/myctx`.
Environment variables use `MYCTX_*`, with legacy `CONTEXT_*` fallbacks. Existing
default `context/config.json` profiles remain usable automatically when the new
`myctx/config.json` file does not exist.

See [the CLI guide](apps/context/README.md) for profiles, automation, all command
groups, request payloads, and installation. Browser login requires the accompanying
gateway changes to be deployed; the CLI detects missing authentication support
before opening a browser.

## CI / CD

GitHub Actions is the source of truth for CI. The single workflow at `.github/workflows/ci.yml` calls local scripts so checks can be reproduced outside Actions:

```bash
bash scripts/ci.sh
```

The workflow runs Swift CLI formatting, unit and HTTP integration tests, and build, the Bun/Turbo workspace checks, and the Swift tests/builds for `services/mcp-gateway`. Railway's GitHub integration deploys `dev` to Development and `main` to Production once those checks pass.

### Deployment

- **Development**: Railway hosts Web, Gateway, and Postgres. Configuration lives under `railway/`; see `docs/runbooks/supabase-to-railway-dev.md`.
- **Production**: Railway hosts Web, Gateway, and Postgres in an isolated `production` environment. See `railway/README.md` and `docs/runbooks/supabase-to-railway-production.md`.
- **Retired infrastructure**: the previous Fly gateway, Supabase database, and Vercel frontend were permanently removed after the Railway cutover was verified. They are not rollback targets.
- **Optional self-hosting**: `services/mcp-gateway/docker-compose.yml` remains as a Portainer/Compose reference.

## History

The backend retains the original repo history. The frontend was merged in via `git subtree` from `Stygian-Tech/my-context-protocol-frontend@dev`; that repo is archival after cutover.

## License

This repository is released under the [MIT License](LICENSE).
