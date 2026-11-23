# Explicit production schema job

These configurations are **not** the application's configuration. They run an
operator command once during a remote Production build, where Vercel injects
its existing Production Secret environment variables. No database credential is
pulled locally, printed, placed in an argument, or returned as an artifact.

The root `vercel.json` is unchanged. Never add this command to normal application
builds/startup, never promote an operator deployment, and never use a Preview
build or copy production credentials into Preview. The database host is pinned
independently in both configurations. The command also checks `VERCEL_ENV` and
the exact project's system ID before accessing the database.

Before starting, the release operator must stop and drain **all** production
writers: production/old deployment URLs, custom/tenant hosts, provider callbacks,
webhooks, MCP telemetry, cron and any direct database jobs. Vercel project pause
blocks production traffic but is not proof that an in-flight function has ended
or that a separately reachable old deployment cannot write. Confirm the actual
Function maximum duration, wait for drain, verify all ingress, and prevent
concurrent deployments/jobs throughout both stages. `--writers-paused` is an
explicit operator declaration; this tool does not install a database lock.

Run from the repository root. Keep the private directory **outside the source
checkout** so the private key and plaintext backup can never enter a deployment:

```sh
umask 077
mkdir -m 700 /private/tmp/myctx-production-operator-20261122
cd services/go
go run ./cmd/production-operator keygen \
  --directory /private/tmp/myctx-production-operator-20261122/keys
cd ../..
cp /private/tmp/myctx-production-operator-20261122/keys/recipient.pem operators/recipient.pem
```

Inspect deployment inputs first (`vercel deploy --dry --json`) and verify no
private key, plaintext snapshot, local environment, or existing backup is in the
upload. `operators/recipient.pem` is a public key; the private key stays local.
Use the existing linked project and target **Production without assigning domains**:

```sh
vercel deploy --prod --skip-domain --force \
  --local-config operators/production-snapshot.vercel.json
```

Do not use `--prebuilt`: the remote build needs the existing Production Secrets.
The only deployment files are `snapshot.enc` (RSA-OAEP/SHA256 wrapped random
AES256-GCM ciphertext) and `status.json` (snapshot fingerprint, ciphertext hash
and independently pinned host). The plaintext is kept only in a private remote
temporary directory and removed before the build completes. Retain deployment
protection. Download the encrypted file over authenticated HTTPS into the private
local directory; do not print the binary response or backup rows into tool logs.
Match the downloaded ciphertext SHA256 against `status.json`.

Then decrypt and rehearse recovery locally:

```sh
cd services/go
go run ./cmd/production-operator review \
  --expected-host my-context-protocol-production-countablenewt.aws-us-east-2.turso.io \
  --key /private/tmp/myctx-production-operator-20261122/keys/recipient-private.pem \
  --envelope /private/tmp/myctx-production-operator-20261122/snapshot.enc \
  --directory /private/tmp/myctx-production-operator-20261122/reviewed
cd ../..
cp /private/tmp/myctx-production-operator-20261122/reviewed/review.json operators/review.json
```

`review` refuses broad private-key permissions, authenticates/decrypts the backup,
restores a private SQLite database, and independently compares exact typed rows,
foreign keys and integrity using the existing snapshot tools. It signs the
resulting snapshot ID and ciphertext hash using RSA-PSS/SHA256. No signed review
is produced if reconstruction fails. All local outputs are exclusive mode0600;
existing evidence is never replaced. Keep the original backup and private key.

Only after reviewing that successful reconstruction, run the separate apply job:

```sh
vercel deploy --prod --skip-domain --force \
  --local-config operators/production-apply.vercel.json
```

Apply verifies the locally signed receipt using the same public recipient. It
exports the paused database again and requires the **exact** reviewed snapshot ID
before running the existing explicit `cmd/migrate` registry. Any intervening data
or schema change aborts before migration. After migration it exports again and
compares every preexisting application table's columns, row count and typed
multiset digest. Migration-history schema changes are handled by the existing
migration/snapshot validators. Only aggregate `status.json` is deployed. A failed
job never claims completion: keep writers paused and inspect/recover explicitly.
The existing migration registry supports idempotent retries, but a partially
applied schema changes the reviewed snapshot ID, so obtain and verify a fresh
backup before another apply job.

Resume/promote only the separately built and reviewed **application** deployment
following the release runbook. Never promote these static operator deployments.
Remove local ephemeral `operators/recipient.pem` and `operators/review.json` after
use; keep private recovery artifacts outside Git. Retire encrypted operator
artifacts after the recovery retention period.

Official references: [Production staging](https://vercel.com/docs/cli/deploy#skip-domain),
[Secret variables](https://vercel.com/docs/environment-variables/sensitive-environment-variables),
[project pause](https://vercel.com/docs/projects/managing-projects#pausing-a-project).
