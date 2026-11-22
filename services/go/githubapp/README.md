# GitHub App and repository integration

The package uses standard-library RSA/RS256 App JWTs, bounded GitHub HTTP
requests, no credential-bearing redirects and no process-local token cache.
`LoadPrivateKey` supports PKCS#1/PKCS#8 PEM, literal newline environment values,
single-line PEM and base64/base64url environment encoding. RSA keys must be at
least 2048 bits. Installation tokens never silently downgrade to OAuth tokens;
404 responses clear stale installation references.

`Service.InstallationToken` matches the sync callback signature.
`ConnectRepository` and `Writeback` match the control service hooks. Both
independently verify project ownership and acquire a database-backed project
operation lease. Apply `MigrationSQL` before starting the service.

Repository connection verifies actual GitHub access before provisioning a signed
push webhook, stores the replacement connection only after GitHub succeeds, and
removes a newly created hook if persistence fails. Previous hooks are deleted
only after the replacement is committed. Permission errors and repositories
omitted from an App installation are reported explicitly; everyone has the same
product access. Repository listing preserves permission fields internally and
bounds pagination to ten pages on the exact GitHub API origin/path.

Writeback reads canonical metadata from a project-owned release/skill, requires
completed runtime clarification, merges only `.mycontext/skills.yaml`, preserves
unknown policy fields and unrelated skills, creates a new branch and opens a
**draft** pull request. It never reconstructs or edits SKILL.md. Existing and
merged policy documents have a 256 KiB limit. A failed external step is reported;
an already-created branch or PR can remain after a later network/database failure.

HTTP routes:

- `GET /github/repos`: signed-in user's accessible repositories.
- `GET /auth/github/app/install`: requires session, owned project, allowlisted
  return origin; relative project returns resolve against the actual allowlisted
  request origin. The Next BFF stamps `X-Context-Frontend-Origin` from its actual
  request URL, overwriting caller input; Go accepts only exact configured frontend
  origins. This header grants no authentication or project access. Direct requests
  can use an allowlisted Host; forwarded-host/protocol headers are ignored. Stores a one-hour,
  random, database-backed install intent.
- `GET /auth/github/app/callback`: requires matching session/account/project,
  unexpired unused intent, and GitHub-verified installation ownership. Optional
  installation OAuth codes are exchanged only for the matching GitHub user and
  encrypted before storage. Intent consumption and account/connection updates
  share one transaction. Replays, arbitrary return origins and mismatched users
  are rejected.
- `POST /webhooks/github-app`: verifies HMAC-SHA256 on exact raw bytes before
  lifecycle handling. Uninstall/suspend and authorization revocation clear stale
  local references. Pushes delegate unchanged signed bytes to `Config.PushWebhook`;
  configure sync's global secret to the same App webhook secret. Repository hook
  signatures continue to work alongside that global secret.

Explicit limitations requiring acceptance work:

- Only personal (`account.type == User`) installations belonging to the signed-in
  GitHub user are attachable, matching the Swift callback. Organization-owned
  installation authorization is not implemented.
- Legacy signed-state/session-fallback recovery is omitted. The current Swift
  database-intent flow is supported; missing/invalid state fails closed.
- Origin-relative installer URLs keep branch aliases and immutable preview hosts
  on the browser's current origin. GitHub's static App Setup URL still needs an explicit secure relay for isolated Vercel
  branch previews. A production callback cannot access a preview database intent
  or preview host cookie. No insecure cross-preview fallback is enabled.
- OAuth user-token refresh, reconnect UI acceptance, orphan branch/hook cleanup
  after interrupted requests, and live GitHub installation acceptance remain to
  be verified. API failures never report a connection or PR as successful.

Tests use in-memory transports and the exact Swift SQLite baseline. They cover
JWT signatures, PEM normalization, token identity, scoped permissions/access,
credential-safe pagination, webhook secrets/signatures/lifecycle, encrypted OAuth
storage, session binding, expiry/replay, ownership checks, real connection SQL and
sidecar-only draft PR creation. No external network is required.

## Provisioning evidence (2026-11-22)

Read-only Railway Gateway variable-name inventories for both Development and
Production contained the GitHub OAuth client id/secret and callback variables,
but **no `GITHUB_APP_*` variables**. Existing OAuth sign-in credentials do not
provision an App. App installation/token/webhook acceptance therefore still needs
an actual GitHub App plus `GITHUB_APP_CLIENT_ID`, private key (raw or base64),
`GITHUB_APP_SLUG`, `GITHUB_APP_WEBHOOK_SECRET`, and registered Setup callback.
`GITHUB_APP_CLIENT_SECRET` is also required when installation requests user OAuth
authorization. No secret values were displayed and no provider settings changed
for this inventory.
