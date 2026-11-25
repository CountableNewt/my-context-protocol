# Temporary signed production operator service

Vercel project pause blocks **new builds**. Deploy this temporary, unaliased
Production service to READY **before** pausing; then invoke its existing endpoint
explicitly. Never promote it or add its route to the normal application. The
normal application has no migration/startup hook.

The original static-build approach is superseded: selecting `--local-config`
alone retained the root Services build remotely, the Other/static build image had
no Go toolchain, and project pause blocked a newly submitted build. The service
below uses the verified Go service framework and runs its fixed migration registry
in process, with no shell, arbitrary SQL or child executable.

Create a minimal source from the reviewed checkpoint, replacing its root config:

```sh
umask 077
mkdir -m 700 /private/tmp/myctx-production-operator-source-20261122
git archive HEAD services/go operators .vercelignore | tar -x -C /private/tmp/myctx-production-operator-source-20261122
cp operators/production-service.vercel.json /private/tmp/myctx-production-operator-source-20261122/vercel.json
```

Keep all private material outside the source/upload directory:

```sh
cd services/go
go run ./cmd/production-operator keygen \
  --directory /private/tmp/myctx-production-operator-20261122/keys
cd ../..
```

Deploy the isolated source to the existing project with `--prod --skip-domain`.
Supply only the base64-encoded **public** `recipient.pem` as deployment-scoped
`OPERATOR_RECIPIENT_PUBLIC_KEY_BASE64`; the private key never leaves the local
private directory. Inspect the upload with `vercel deploy --dry --json` first.
The existing Production database Secret remains inside Vercel; it is never pulled,
logged, passed in an argument, or returned in any response. Runtime checks enforce
the exact project, Production environment, independent database hostname and the
unique deployment hostname (`VERCEL_URL`). Retain Vercel Authentication.

Use a new private request directory for each invocation. From `services/go`:

```sh
go run ./cmd/production-operator invoke --mode preflight \
  --audience TEMPORARY-OPERATOR-DEPLOYMENT.vercel.app \
  --expected-host my-context-protocol-production-countablenewt.aws-us-east-2.turso.io \
  --key /private/tmp/myctx-production-operator-20261122/keys/recipient-private.pem \
  --directory /private/tmp/myctx-production-operator-20261122/preflight
```

The CLI creates an RSA-PSS signed request and invokes the local `vercel curl`
client, which handles its own Vercel deployment authentication. It saves the
response and headers privately. `sign` writes only the request for an operator who
wants to invoke it separately. Signatures bind POST `/operator`, exact command,
deployment audience, database host, nonce, issued/expiry times, pause declaration
and reviewed backup receipt. Requests are strict JSON <=32KiB and valid <=15min.
Preflight performs only a database reachability/migration-count SELECT; it does
not consume a nonce or acquire a lease. Confirm this succeeds before the outage.

The release operator owns stopping all production ingress, old deployment URLs,
webhooks, callbacks, MCP writers and scheduled maintenance. Pause alone is not
proof that previous invocations drained: the current deployed functions have a
verified 300-second platform timeout. Confirm blocked ingress and wait at least
that bound plus propagation margin. This service never pauses or resumes traffic.
After pause, prove the staged operator endpoint remains reachable **before** any
migration; if it does not, unpause and resolve access first.

Take a backup with the same command and new directory, adding
`--mode snapshot --writers-paused`. Snapshot exports the frozen database, gzips
it, and returns only RSA-OAEP/SHA256 wrapped AES256-GCM ciphertext. The authenticated
V2 envelope limits decompression to256MiB; V1 raw envelopes remain readable. The
HTTP ciphertext is capped at4MiB (below the platform4.5MiB response bound), with
aggregate proof in `X-Myctx-Operator-Proof`. Oversized snapshots fail explicitly
before any schema change. Responses are private/no-store; SQL/source values are
never emitted in errors or logs.

Review `snapshot/response.body` locally:

```sh
go run ./cmd/production-operator review \
  --expected-host my-context-protocol-production-countablenewt.aws-us-east-2.turso.io \
  --key /private/tmp/myctx-production-operator-20261122/keys/recipient-private.pem \
  --envelope /private/tmp/myctx-production-operator-20261122/snapshot/response.body \
  --directory /private/tmp/myctx-production-operator-20261122/reviewed
```

Review authenticates/decrypts, restores an offline SQLite database and independently
checks integrity, foreign keys and every exact typed table digest. Only after
success does it sign `review.json`, binding the host, snapshot ID and ciphertext
hash. Existing private evidence is never overwritten.

Apply using another new request directory, `--mode apply --writers-paused` and
`--receipt /private/tmp/myctx-production-operator-20261122/reviewed/review.json`.
The server verifies that independent signed review, exports the database again,
and requires the exact reviewed source fingerprint before applying the shipped
`appschema.Registry`. It then exports again and compares every previous
application table's columns, count and typed digest, including unchanged account
values across the nullable GitHub identity schema rebuild. Only aggregate success
evidence is returned. Any ambiguous/failed apply requires inspection while
writers remain paused; never infer success from a lost response or blindly retry.

Authenticated snapshot/apply requests use an atomic durable nonce and a300-second
fenced lease in existing private `mcp_rate_limits`. No coordination DDL runs.
Only buckets beginning with this exact fresh recipient's
`operatorjob:<SHA256(DER public key)>:` prefix are excluded from backup/fingerprint
reads; ordinary quota buckets, other operator recipients and prefix strings that
occur elsewhere in a key remain intact. This prevents disposable operator
coordination from invalidating the reviewed source fingerprint. The snapshot
adapter is read-only. Successful operations release their lease; uncertain errors
retain it until expiry. Root release cleanup removes **only this exact prefix**.

After verified application promotion/acceptance, delete the temporary operator
deployment, remove its public-key deployment input, and retire its exact-prefix
coordination rows. Keep the private backup/key per the recovery retention policy.
Never promote the operator service or publish a plaintext snapshot.

References: [Production staging](https://vercel.com/docs/cli/deploy#skip-domain),
[project pause](https://vercel.com/docs/rest-api/projects/pause-a-project),
[Function payload limits](https://vercel.com/docs/functions/limitations).
