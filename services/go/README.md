# Go Backend Foundation

Shared Go packages for the incremental Swift gateway migration. This module does
not replace the production gateway yet and does not claim HTTP/MCP route parity.

- `identity`: browser and CLI GitHub login, persistent sessions and handoff confirmation; see its README for the exact implemented routes and outstanding hosted acceptance.
- `reposync`: bounded GitHub archive fetch/extraction and recursive skill discovery; pipeline publication is still pending.
- `security`: AES-256-GCM encrypted repository tokens compatible with Swift
  Crypto's base64 nonce/ciphertext/tag format; lowercase SHA-256 token digests;
  32-byte random opaque tokens.
- `session`: the JSON string-to-string payload stored in `app_sessions`. Existing
  account identity uses the `accountId` key. Updating a session must use an UPDATE
  that fails when no session exists; never upsert a revoked session.
- `turso`: stateless Hrana HTTP `/v2/pipeline` requests, explicit conditional batch
  transactions and same-request stream close. SQL arguments use typed values;
  integers remain decimal strings. Each client has a 15-second HTTP timeout and
  respects an earlier request context deadline. Requests and responses are capped
  at 4 MiB and transactions at 128 application statements. Redirects are rejected.

The Turso client accepts `libsql://` or HTTPS origins and an auth token. Loopback
HTTP is allowed for local tests. Tokens are not included in error strings. SQL
must be application-owned and transaction-control statements must not be passed
to `Batch`. No automatic HTTP retry is attempted: a lost response may follow a
successful commit, so mutations require application-specific idempotency before
retrying. Database migrations run as an explicit release/setup task, never from
request initialization.

```sh
cd services/go
go test -race ./...
go vet ./...
```

The HTTP tests use local test servers, without contacting a real Turso database.
Production cutover still requires a remote Turso transaction/rollback rehearsal,
Swift-produced encrypted-token fixtures, and authentication/session route parity.

Protocol references:

- <https://github.com/tursodatabase/libsql/blob/main/docs/HTTP_V2_SPEC.md>
- <https://github.com/tursodatabase/libsql-client-ts/blob/main/packages/libsql-client/src/hrana.ts>
