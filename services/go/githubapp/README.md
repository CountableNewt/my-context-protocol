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
- `GET /auth/github/app/callback` (also UUID install-intent states on shared
  `/auth/github/callback`): requires an unexpired unused intent bound to its
  original account/project, and GitHub-verified installation ownership. Code-less
  Setup callbacks require the matching session. Code-bearing installation callbacks
  can cross deployment aliases without a cookie only after OAuth `/user.id` matches
  the saved intent account and the App JWT verifies personal installation ownership.
  Any session that is present must also match; stale/mismatched sessions fail closed.
  Optional
  installation OAuth codes are exchanged only for the matching GitHub user and
  encrypted before storage. If GitHub OAuth-on-install omits `installation_id`,
  the verified App user token lists accessible installations; one matching
  personal installation is then independently verified with the App JWT. Ordinary
  signed OAuth states remain on the normal browser/CLI login handler.
  Intent consumption and account/connection updates
  share one transaction. Replays, arbitrary return origins and mismatched users
  are rejected.
- `POST /webhooks/github-app`: verifies HMAC-SHA256 on exact raw bytes before
  lifecycle handling. Uninstall/suspend and authorization revocation clear stale
  local references. Pushes delegate unchanged signed bytes to `Config.PushWebhook`;
  configure sync's global secret to the same App webhook secret. Repository hook
  signatures continue to work alongside that global secret. The sync entrypoint
  also dispatches recognized App lifecycle events received at legacy
  `/webhooks/github` to this same signature-checked handler; repository push and
  ping stay on the repository verifier.

Explicit limitations requiring acceptance work:

- Only personal (`account.type == User`) installations belonging to the signed-in
  GitHub user are attachable, matching the Swift callback. Organization-owned
  installation authorization is not implemented.
- Legacy signed-state/session-fallback recovery is omitted. The current Swift
  database-intent flow is supported; missing/invalid state fails closed.
- Origin-relative installer URLs and verified code-bearing callbacks support
  branch aliases and immutable URLs sharing the same preview database. GitHub's
  registered user-authorization callback must still reach that database. A fixed
  production callback cannot recover an isolated preview's DB intent; use a
  dedicated preview callback or a deliberately designed cross-database broker.
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
Production contained `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` and the OAuth
callback, but no `GITHUB_APP_*` aliases. A later inspection established that the
existing development sign-in registration is already a **GitHub App** with
OAuth during installation enabled. A new App registration is not required.

Configure the explicit App client id/secret aliases to that existing App pair,
`GITHUB_APP_SETUP_CALLBACK_URL` to the registered shared user-authorization
callback, and `GITHUB_APP_SLUG` to the existing App slug. App JWT operations also
require a private signing key; signed App webhooks require the configured App
webhook secret. Preserve existing keys and production settings during preview
acceptance. Do not infer that absent environment aliases mean the App itself is
absent.

OAuth-on-install uses the App's first user-authorization callback and disables the
separate Setup URL. Both ordinary sign-in and install-intent callbacks are now
supported on that shared callback. Code-bearing callbacks can securely cross aliases within the same isolated
preview database: the DB nonce plus verified GitHub OAuth identity replaces the
missing cross-host cookie, while any present session must still match. Return URLs
come only from the saved allowlisted project intent. A callback using a different
preview database still cannot recover that intent. No latest-intent fallback or
state-only authentication is enabled.

References: [GitHub App registration settings](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration),
[OAuth during installation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).
No secret values were displayed and no provider settings changed for this audit.
