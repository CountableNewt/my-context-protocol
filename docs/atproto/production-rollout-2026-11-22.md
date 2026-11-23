# Production rollout checkpoint — 2026-11-22

The owner authorized the production rollout and subsequently chose to retain
Vercel Hobby and wait for the deployment quota reset. No billing upgrade was
made. PR #85 remains a draft until the remaining acceptance/publication gates pass.

## Live state

- Original application remains active at `mycontextprotocol.dev`,
  `api.mycontextprotocol.dev`, mapped MCP hosts and `mcp.samclemente.me`.
  Deployment: `dpl_ENNFGveWR71rmds5MhcAJiUXpnjR`.
- Production HTML returns 200 and the unauthenticated dynamic account route
  returns 401. Chrome GitHub sign-in succeeded as the existing account.
- No production schema migration, account-data publication or product Lexicon
  record publication has run. Last verified migration count is **5**.
- Production `ATPROTO_ENABLED` and `TANGLED_ENABLED` are **false**. The new
  production-only assertion key and reviewed public configuration are stored in
  Vercel; private key/token values are not recorded here.
- A separately staged application is READY at deployment
  `dpl_DqxcU9GorsRYjxYKaQrXtPH5TJXL` (source checkpoint `01f4d17`). It has its
  intended activation settings but is **not promoted**. Its OAuth metadata was
  checked for the exact production client ID, DPoP and public-only JWKs.
- The task-created automation protection bypass was revoked while waiting.
  Deployment SSO protection remains configured.

## Namespace DNS is published

The actual authoritative DNS provider is Marque, using `cirrus.mqdns.at`,
`nimbus.mqdns.app` and `stratus.mqdns.de`; Vercel DNS is not authoritative.
The selected authority remains `did:plc:qy5pluw2bsuq2x6albsgkvx3` (`samclemente.me`).

All 15 capability groups have their own `_lexicon` TXT entry pointing to that DID,
with TTL 60. The 28 original zone entries were preserved; there are now 43 entries.
All 60 checks (15 names across the three authorities and public resolver 1.1.1.1)
returned the intended DID. DNS zone record:

`at://did:plc:qy5pluw2bsuq2x6albsgkvx3/at.marque.dns/mycontextprotocol.dev`

Verified CID: `bafyreieqmcklatjpmke6x7lekoxywcooo2jk4626c4s75qj36qtdamitsq`.
DNS delegation alone does not establish schema publication. The 96 product schemas
remain pending PDS publication and independent CID verification.

## Verified provider capabilities

The actual Go public Tangled client fetched and cleaned up the read-only skills
mirror successfully, resolving the immutable revision. Bobbin's deployed
`getBranch` returned 404; the adapter now falls back to bounded authenticated
`listBranches` only for 404, preserving authentication denials. The exact new RPC
grant is included in the prepared configuration. The actual archive redirect uses
`https://mirror.tangled.network`; the Go client succeeded without a user-agent change.
Use a separate disposable repository for proposal/writeback acceptance, never the
existing read-only mirror.

## Explicit operator preparation

Sensitive Turso credentials remain unreadable through Vercel's export API, but a
read-only Production job successfully accessed the independently pinned production
host inside Vercel without extracting them. Its aggregate preflight was verified.

Observed platform constraints changed the execution mechanism:

1. `--local-config` retained the normal root Services configuration remotely.
2. An isolated Other/static builder had no Go toolchain.
3. Vercel project pause blocks **new builds**, though an already-ready staged
   static operator artifact remained accessible during pause.

The superseding mechanism is the temporary signed Go operator service documented
in [operators/README.md](../../operators/README.md). It must be deployed READY and
preflighted before the next pause. It is never promoted or routed through the
normal product. RSA-PSS request signatures, exact audience/host/time binding,
durable nonce/lease checks and the existing Vercel protection restrict access.
Snapshot responses contain only bounded gzip-plus-RSA/AES-GCM ciphertext. Local
recovery rehearsal signs a reviewed source fingerprint; apply rechecks it and
verifies all preexisting typed rows after running the fixed migration registry.

The operator's disposable coordination rows use a fresh public-key-specific private
quota prefix. Only that exact prefix is excluded from operator snapshot reads;
all normal and other-generation quota rows are preserved. Retire the operator
endpoint before removing its nonce rows, or allow normal private quota retention
to expire them. Never remove active replay protection.

A short pause was tested, all four production ingress hosts returned 503, and the
old application was restored when the build restriction was discovered. No snapshot
or migration was attempted during that pause. Actual production function timeouts
were verified at 300 seconds; use at least that drain plus a propagation margin
on the next maintenance window. The current service is **not paused**.

## Resume after quota reset

Vercel rejected deployment creation with `api-deployments-free-per-day` (100 per
86,400 seconds). No exact reset timestamp was returned. Do not retry continuously,
upgrade billing, change teams or promote the staged application to evade this gate.

1. Confirm the latest source/CI and the original live deployment, flags and secret
   isolation. Deploy only the temporary operator, `--prod --skip-domain`, with its
   deployment-scoped public recipient key. Verify its effective timeout, outer
   protection, signed preflight and rejection of unsigned requests.
2. Pause and verify every production writer ingress; confirm the staged operator
   still responds. Drain at least 315 seconds from confirmed blocking.
3. Obtain an encrypted snapshot, decrypt and restore it locally, and verify every
   typed digest before issuing the signed apply request. Retain the private recovery
   bundle outside Git and all deployment source. No backup has yet been taken.
4. Apply and verify migrations 006–016, exact existing row preservation and recovery.
   Promote only the separately reviewed application, then resume traffic and check
   existing GitHub/MCP behavior. Keep a compatible recovery deployment available.
5. Complete Chrome/CLI ATProto authorization and linking, actual Tangled public and
   private acceptance, owner-reviewed canonical publication/reconstruction,
   generated-client XRPC calls and mapped tenant-host MCP checks. A locked Mac and
   any required new OAuth consent must be handled by the owner when prompted.
6. Publish the reviewed 96 schema records under the authority account, independently
   verify all DNS/PDS CIDs, then record publication evidence/readiness. Existing
   telemetry consent does not authorize public publication of existing data.
7. Retire the temporary operator and task-created automation credentials. Record
   the final deployment, migration, acceptance and recovery evidence before closing
   the rollout.

Private prepared keys/configuration and local command evidence currently reside
under `/private/tmp/myctx-production-operator-20261122-atproto`; they are not in the
repository or ordinary preview deployments. Retain recovery material durably once
an actual production backup is created. Never print credentials or upload plaintext
production data to a preview.
