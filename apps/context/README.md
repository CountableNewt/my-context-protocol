# MyContextCLI

MyContextCLI (`myctx`) is the Swift CLI for MyContextProtocol. It exposes every current account,
project, and administrator management route to people and agents. Success output
is JSON on stdout; errors are readable text on stderr by default. Add `--json`
for structured errors in agent scripts. It uses Swift Foundation and platform socket APIs, with no third-party packages.

## Install

Requires Swift 6 or newer on macOS or Linux. Tests also require Python 3. From this repository:

```sh
cd apps/context
make install                     # installs ~/.local/bin/myctx (must be on PATH)
# Or: make build                 # produces bin/myctx
myctx version
```

After a successful install, `myctx` is available from any working directory.
The installer checks that its destination is on `PATH` before building. If needed,
run `export PATH="$HOME/.local/bin:$PATH"`, add that line to `~/.zshrc` (or your
shell's startup file), and rerun `make install`. No shell configuration is edited
automatically. A custom destination is supported with `make install BINDIR=/path/on/PATH`
or `PREFIX=/prefix` (installs into `/prefix/bin`); use an absolute destination
whose exact spelling appears as a `PATH` entry.

Build a versioned binary with `make build VERSION=0.1.0`. Build on each target platform with its Swift toolchain. The installed executable needs no Node or Go runtime; Linux hosts require the Swift runtime libraries supplied by their Swift installation.

## Authenticate

MyContextCLI uses the hosted account API at `https://api.mycontextprotocol.dev`
by default. Start with:

```sh
myctx auth login
myctx auth status
```

For self-hosting or an explicit Development environment, set `--url`, `MYCTX_URL`,
or a saved profile URL. Use the account gateway/API origin; your personal tenant
MCP endpoint and the dashboard origin do not provide the account management API.

Login opens GitHub in your browser. MyContextCLI binds an ephemeral port on
`127.0.0.1`, validates a random callback nonce, exchanges the single-use handoff,
and stores the account session in your local config file. `--no-browser` prints
the login URL for manual opening on the same machine. Login expires after five
minutes; Ctrl-C cancels. Browser login requires the gateway's `/auth/cli/github`
route included in this change. The CLI checks that the gateway supports CLI
authentication before opening a browser, so a missing route produces an immediate
error. Hosted browser login still requires deployment of the accompanying gateway
changes. Until those changes are deployed, use session import instead.

The gateway supplies its configured frontend origin during login so subsequent
mutations satisfy the existing CSRF policy. Older gateways/session imports need
`--web-url https://YOUR_DASHBOARD_ORIGIN` or `MYCTX_WEB_URL`.

For an existing account session:

```sh
# Pipe the raw vapor-session cookie value from a secure credential source:
credential-source | myctx auth login --session-stdin --web-url https://YOUR_DASHBOARD_ORIGIN
```

For ephemeral agent/CI authentication, inject `MYCTX_SESSION` and, when required,
`MYCTX_WEB_URL` from a secret manager. Set `MYCTX_URL` when overriding the hosted
account API. A project MCP API key is not an
account administration credential. MyContextCLI does not read stdin unless explicitly
requested with `--session-stdin` or `--data -`. It never asks interactive questions.
`auth logout` revokes the active session and removes the stored credential;
unset `MYCTX_SESSION` (and legacy `CONTEXT_SESSION`, if set) separately if you
provided it through the environment.

Config lives at the platform user-config directory under `myctx/config.json`
(macOS: `~/Library/Application Support/myctx/config.json`). `--config FILE` or
`MYCTX_CONFIG` overrides it (the flag takes precedence). New directories use mode 0700 and files mode 0600
on Unix. If the new default file does not exist and the old
`context/config.json` file does, MyContextCLI transparently continues using the old
file, preserving saved sessions and profiles. Explicit config paths bypass this
default-path fallback. Sessions are plaintext in this private file; use environment injection
when persistence is undesirable. Config writes are atomic. Concurrent writes to
the same config file are not supported.

Environment variables use the `MYCTX_*` prefix: `MYCTX_URL`, `MYCTX_WEB_URL`,
`MYCTX_SESSION`, `MYCTX_PROFILE`, `MYCTX_PROJECT`, and `MYCTX_CONFIG`. The previous
`CONTEXT_*` names remain supported as fallbacks when their corresponding
`MYCTX_*` variable is unset or empty. In particular, `MYCTX_CONFIG` takes priority
over `CONTEXT_CONFIG`. The repository folder and Swift package remain `apps/context`;
the executable is `myctx`.

## Profiles and defaults

```sh
myctx --profile development config set --url http://localhost:8080 --web-url http://localhost:3000
myctx --profile development auth login
myctx --profile development config set --project PROJECT_UUID
myctx --profile development projects get
myctx config show
```

Flags take precedence over environment variables, which take precedence over the
selected profile, then the hosted API default `https://api.mycontextprotocol.dev`.
An existing profile with an explicit URL keeps that URL. Profile defaults to
`default` (`MYCTX_PROFILE` overrides it).
`--id` overrides `--project`, then `MYCTX_PROJECT`, then the stored project.
Credentials and project defaults are scoped to the API origin. Changing a
profile's URL clears its stored session and project. `config show` reports whether
a credential is present without exposing its value.

## Agent discovery and requests

```sh
myctx help
myctx projects --help
myctx commands                  # JSON operation name/method/path/description registry
myctx api operations            # same registry
myctx projects list --compact
myctx projects create --data '{"name":"My Skills","slug":"my-skills"}'
myctx projects update --id PROJECT_UUID --set 'name="Shared Skills"'
myctx repos connect --id PROJECT_UUID --data '{"owner":"example","repo":"skills","branch":"main"}'
myctx projects sync --id PROJECT_UUID
myctx releases list --id PROJECT_UUID
myctx releases validation --id PROJECT_UUID --release-id RELEASE_UUID
myctx releases activate --id PROJECT_UUID --release-id RELEASE_UUID
myctx skills list --id PROJECT_UUID --release-id RELEASE_UUID
myctx runtime update --id PROJECT_UUID --data @runtime-settings.json
myctx logs list --id PROJECT_UUID --query limit=100 --query offset=0
myctx skills usage --id PROJECT_UUID --query page=1 --query page_size=100
```

Flags can appear before or after command words. All response fields and server
validation error details are available with `--json`, including exact JSON integers. Empty successful
responses emit `{"ok":true}`. Non-JSON responses become `{"result":"..."}`.
There are no automatic retries: mutations run once. Set `--timeout 60s` to override
the default 30-second HTTP timeout. Responses are bounded to 32 MiB and request
files/stdin to 8 MiB. Lists follow server pagination: logs use `limit` and `offset`;
skill usage uses `page` and `page_size`. The CLI returns one page per invocation.

Use `--data` for a JSON literal, `--data @file.json` for a file, or `--data -` for
stdin. Repeated `--set field=JSON` merges top-level fields into an object; strings
must be JSON-quoted. Repeated `--query key=value` adds query parameters. Existing
backend schemas govern payload validation; complex runtime/skill payloads can be
passed unchanged from files. No private fields are removed from successful API
responses: API-key creation intentionally returns the secret once.

| Group | Commands |
| --- | --- |
| `auth` | `login`, `status`, `me`, `logout` |
| `dashboard` | `summary`, `timeseries` |
| `projects` | `list`, `create`, `get`, `update`, `set-active`, `summary`, `timeseries`, `sync` |
| `catalog` | `get`, `update` |
| `repos` | `connection`, `connect` |
| `releases` | `list`, `activate`, `validation` |
| `skills` | `list`, `update`, `writeback`, `usage` |
| `runtime` | `get`, `update` |
| `keys` | `list`, `create`, `update`, `revoke` |
| `logs` | `list` |
| `domains` | `get`, `set`, `verify` |
| `billing` | `checkout`, `portal` |
| `github` | `repos`, `install` |
| `admin` | `metrics`, `timeseries`, `rollup-refresh`, `lookup`, `privileged-accounts`, `account-flags` |

Project commands use `--id` (or default project). Release commands additionally
use `--release-id`; skill modifications use `--compiled-skill-id`; key update and
revoke use `--key-id`. GitHub App installation uses `--query project_id=PROJECT_UUID`.
Administrator operations retain the server's privilege checks.

Common payloads:

| Operation | JSON body |
| --- | --- |
| `projects create` | `{"name":"Skills","slug":"skills"}` |
| `projects update` | `{"name":"New Name"}` |
| `catalog update` | `{"markdown":"# Skills"}` (empty markdown resets) |
| `repos connect` | `{"owner":"org","repo":"skills","branch":"main"}` |
| `keys create` / `keys update` | `{"name":"Agent"}` |
| `domains set` | `{"hostname":"context.example.com"}` |
| `billing checkout` | `{"interval":"month"}` or `{"interval":"year"}` |
| `admin lookup` | `{"github_login":"example"}` (also `github_id` or `email`) |
| `admin account-flags` | `{"account_id":"UUID","is_admin":false,"paywall_bypass":true}` |

`skills writeback` creates an external branch and draft pull request; invoking
that command authorizes that action. Billing commands create URLs; they do not
open or complete checkout. GitHub installation returns its browser URL. HTTP
redirects are never followed with account credentials.

For API operations beyond the named registry:

```sh
myctx api GET /projects
myctx api PATCH /projects/PROJECT_UUID --data @project.json
```

Raw requests stay on the configured API origin. This is an application control
CLI; tenant MCP protocol calls, inbound webhooks, internal infrastructure routes,
and browser callback endpoints are not named management commands. Agent shells
can run this CLI directly; it does not require a model-provider account.

Exit codes: `0` success, `1` transport/server error, `2` invalid arguments,
`3` HTTP 401, `4` HTTP 403, `130` interrupted. Default errors include a message,
HTTP status when available, useful server detail, and a next-step hint. Use the
global `--json` flag anywhere among command arguments to retain machine-readable
errors with `code`, `error`, and, for HTTP errors, `status` and original JSON
`details`. Successful output stays JSON with either format. For example:

```sh
myctx --json projects list
```

## Development

```sh
make check
make test
make build
```

The Swift route-coverage test reads `services/mcp-gateway/Sources/App/routes.swift` and
fails when a management endpoint is missing from the registry. Tests therefore
run from the monorepo checkout. The CLI checks run Swift formatting and warnings-as-errors builds, native Swift tests,
and executable integration tests against isolated loopback HTTP servers. The latter cover
browser authentication, session storage, credential isolation, redirects, JSON precision,
request handling and error output. `make build VERSION=...` embeds the version into the
standalone executable. Concurrent versioned builds in the same checkout are unsupported.
