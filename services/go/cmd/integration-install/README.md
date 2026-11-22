# One-use integration installer

This operator utility does not create an integration, accept Vercel's legal
agreement, open an installation page, select projects, or grant permissions.
Those steps require the explicit approval and manual flow described in
`../../domains/README.md`. Do not start it until that approval exists.

Build with `go build -o /private/tmp/mcp-integration-install ./cmd/integration-install`
from `services/go`. Supply one JSON object with `client_id` and `client_secret`
through a private pipe to `/private/tmp/mcp-integration-install --save-production`.
Do not put credentials in command arguments, shell history, temporary plaintext
files or an echoed terminal. The utility rejects terminal stdin. It prints no
credentials, codes, tokens, installation IDs or provider responses.

It binds only IPv4 `127.0.0.1:7338` and prints a fresh unguessable Redirect URL.
First create the integration with a placeholder localhost callback so client credentials
are available. After starting this utility, update the saved integration Redirect URL
to its exact printed URL before installing it.
Manually install on Sam Clemente's Team, selecting only MyContextProtocol, with
mandatory Integration Configuration Read plus Project Read/Write and Domain Read/Write permissions. The registered random
callback path acts as a one-use installation nonce; no undocumented state-query
forwarding is assumed. Restarting generates a new path requiring form update.

On callback, it checks method, exact Host, loopback peer and nonce path, exchanges
one code with Vercel over bounded HTTPS, and verifies expected team, permitted
scopes from the configuration endpoint (an array, rather than an assumed token
response field), and exactly one selected project matching the hardcoded
MyContextProtocol project ID. An all-project selection is rejected. Token/installation error responses remain private. The callback URL
uses no external assets and sets no-store/no-referrer headers.

Only after those checks does the utility run the authenticated local Vercel CLI
with fixed project ID, explicit team scope, `--environment production --type secret`.
It sends each value through stdin, suppresses CLI output, never passes values as
arguments, and never overwrites existing variables (`--force` is absent). It saves:

- `VERCEL_INTEGRATION_CLIENT_ID`
- `VERCEL_INTEGRATION_INSTALLATION_ID`
- `VERCEL_INTEGRATION_CLIENT_SECRET`

The secret is saved last. Vercel environment writes are not atomic; a failure
reports that partial names may exist, requiring names-only inspection and deliberate
recovery. It does not delete variables on failure, save access tokens, add preview
credentials, set `VERCEL_TOKEN`, redeploy or mutate domains. Keep the integration
client credentials available through the approved secure mechanism until success.
Runtime obtains fresh one-hour tokens independently.

The utility exits after one callback or 30 minutes. Its tests use local fixtures
and never invoke the Vercel CLI or create an integration.
