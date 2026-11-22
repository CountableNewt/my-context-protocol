# Go gateway services

The migration replaces the Swift gateway with four HTTP services in one Vercel
deployment. Production cutover status and acceptance evidence live in
[`docs/migrations/go-vercel-turso.md`](../../docs/migrations/go-vercel-turso.md).

| Command | Responsibility |
| --- | --- |
| `cmd/identity` | GitHub browser/CLI login, sessions and installation callbacks |
| `cmd/control` | Account/project APIs, catalogs, analytics and maintenance |
| `cmd/mcp` | Tenant-bound MCP, OAuth consent/PKCE and streaming notifications |
| `cmd/sync` | Signed webhooks, bounded repository sync and release publication |
| `cmd/migrate` | Explicit schema initialization or verified legacy adoption |
| `cmd/rehearse` | Disposable synthetic checks against the named preview database |
| `cmd/import` | Offline typed snapshot export, staged transfer and verification |

Run `bash scripts/check-go.sh` from the repository root. Integration tests require
Python 3 and exercise real SQLite transactions; production uses stateless Turso
HTTP requests and has no Python or local SQLite dependency.

Copy `env.example` into your local environment when running services individually.
Services never apply schema migrations during startup. Run the migration command
against an explicitly selected database before serving traffic. Initialization and
legacy adoption are separate operations; retain a consistent source backup first.

Vercel settings are in the root `vercel.json`. Next.js uses private runtime service
bindings; browser requests use the same origin under `/api`. The hosted MCP path is
`/mcp`; self-hosted deployments can configure a different path. Tenant OAuth needs a
mapped tenant hostname, not just a generic deployment URL.

Preview credentials belong only to their Git branch and isolated database. Preserve
the original encryption key when importing existing encrypted GitHub tokens. Never
put production credentials or user data in an ordinary branch preview.

GitHub Apps using OAuth during installation can share `/auth/github/callback` with
regular sign-in. Configure the App's callback to the branch alias for preview, and
the canonical app origin for production. A static callback cannot select arbitrary
isolated branch databases; each such branch needs its own App/callback configuration
or a separately reviewed callback broker.

The shared security package uses Swift-compatible AES-256-GCM base64
nonce/ciphertext/tag serialization and lowercase SHA-256 credential hashes.
Session payloads retain the `accountId` key; updates never upsert revoked sessions.
The Turso client rejects redirects and arbitrary transaction-control statements,
closes streams in the same request, caps request/response bodies at 4 MiB, and
allows at most 128 application statements per transaction. It does not retry
mutations automatically: a lost HTTP response can follow a successful commit.
Retry safety belongs to each operation's durable idempotency state.

Protocol references: [Hrana HTTP v2](https://github.com/tursodatabase/libsql/blob/main/docs/HTTP_V2_SPEC.md)
and the [official client implementation](https://github.com/tursodatabase/libsql-client-ts/blob/main/packages/libsql-client/src/hrana.ts).

For production transfer, follow the [snapshot workflow](snapshotimport/README.md).
