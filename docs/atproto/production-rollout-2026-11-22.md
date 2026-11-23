# Production rollout checkpoint — 2026-11-22

The owner authorized the production rollout and subsequently chose to retain
Vercel Hobby and wait for the deployment quota reset. No billing upgrade was
made. PR #85 remains a draft until the remaining acceptance/publication gates pass.

## Live state

- The reviewed application is promoted and production traffic is resumed at
  `mycontextprotocol.dev`, `api.mycontextprotocol.dev`, mapped MCP hosts and
  `mcp.samclemente.me`. Active deployment:
  `dpl_DqxcU9GorsRYjxYKaQrXtPH5TJXL` (source checkpoint `01f4d17`).
- Production schema migration completed through **016**: the migration registry
  contains **16** entries. All preexisting application table counts and exact typed
  row digests were verified unchanged after migration. The accounts rebuild keeps
  existing values and permits accounts without a GitHub identity.
- The active application has `ATPROTO_ENABLED` and `TANGLED_ENABLED` enabled.
  Production-only assertion material and reviewed public configuration remain in
  Vercel; private key/token values are not recorded here.
- Eight post-promotion HTTP smoke checks passed. Chrome GitHub sign-in succeeded
  as `CountableNewt`, and the dashboard displayed the existing project. These
  checks establish the recorded existing-account regression evidence; they do not
  establish the remaining ATProto/Tangled acceptance gates.
- All 514 staged runtime/contract/configuration source file hashes independently
  matched checkpoint `01f4d17`. Later source changes are operator tooling,
  documentation and the equivalent shared migration registry. Its 15 ordered
  additive migration version/SQL pairs exactly match the staged command.
- No owner product records or product Lexicon schema records have been published.
  Enabling the integration and migrating SQL schema does not migrate an existing
  project's data to public ATProto records.
- Authority linking is paused at Northsky's password sign-in, awaiting the owner.
  The existing GitHub account remains signed in. No password or other credential
  is recorded in this document.
- PR #85 remains a draft until the remaining acceptance/publication gates pass.
  Deployment SSO protection remains configured. Temporary operator retirement and
  task-created automation credential cleanup remain release closeout work.

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

## Explicit operator execution

Sensitive Turso credentials remain unreadable through Vercel's export API, but a
read-only Production job successfully accessed the independently pinned production
host inside Vercel without extracting them. Its aggregate preflight was verified.

Observed platform constraints changed the execution mechanism:

1. `--local-config` retained the normal root Services configuration remotely.
2. An isolated Other/static builder had no Go toolchain.
3. Vercel project pause blocks **new builds**, though an already-ready staged
   static operator artifact remained accessible during pause.

The superseding mechanism is the temporary signed Go operator service documented
in [operators/README.md](../../operators/README.md). It was deployed READY and
preflighted before the successful migration pause. It is never promoted or routed through the
normal product. The temporary operator deployment is
`dpl_4WbPmZo6LhxqYrJHfGLpuipS3tAy`; its effective Go function timeout was
independently verified at 300 seconds. RSA-PSS request signatures, exact audience/host/time binding,
durable nonce/lease checks and the existing Vercel protection restrict access.
Snapshot responses contain only bounded gzip-plus-RSA/AES-GCM ciphertext. Local
recovery rehearsal signs a reviewed source fingerprint; apply rechecks it and
verifies all preexisting typed rows after running the fixed migration registry.

The operator's disposable coordination rows use a fresh public-key-specific private
quota prefix. Only that exact prefix is excluded from operator snapshot reads;
all normal and other-generation quota rows are preserved. Retire the operator
endpoint before removing its nonce rows, or allow normal private quota retention
to expire them. Never remove active replay protection.

An initial short pause was tested: all four production ingress hosts returned
503, and the old application was restored when the new-build restriction was
discovered. No snapshot or migration ran during that first pause. After the quota
reset, the already-ready signed operator service was used for the successful
production pause, backup, verification and migration. Production is now unpaused.
Future maintenance must still stop every writer and drain the verified 300-second
function lifetime plus propagation margin before taking a consistent snapshot.

## Verified backup and migration evidence

The encrypted production snapshot was downloaded, decrypted and restored locally.
The independent SQLite recovery rehearsal verified integrity, foreign keys and
exact typed table digests before the locally signed apply authorization was issued.
The service required a fresh source fingerprint equal to that reviewed backup
before applying migrations 006–016, then verified all preexisting application rows
against a new post-migration export.

- Reviewed pre-migration snapshot ID:
  `fc0bdc78889d45159294857a0699378c2adb45378b3bd02fc19d6716c5a07f5d`.
- Verified post-migration snapshot ID:
  `ae3c66c8a2a0581e9b2bc1e01bdf5b7f246b8ddf0d9e45cdfb018efa27581ee9`.
- Durable private recovery bundle:
  `/Users/sam/.local/share/mycontextprotocol/recovery/atproto-production-20261122`.

The fingerprints differ because the shipped schema changed; unchanged table
columns, counts and typed row digests supplied the source-preservation check.
Only this operator recipient's disposable coordination prefix is excluded, as
documented above. Recovery material is outside Git and deployment source. Do not
print credentials or upload plaintext production data to a preview.

A SQL rollback after new application or PDS writes must reconcile those later
writes. The pre-migration backup alone cannot recover later changes, and restoring
SQL cannot recall records already published or replicated through ATProto.

## Remaining acceptance and closeout

The Hobby deployment quota cleared without a billing upgrade or project/team
change. The migration and application promotion are complete; the following
work remains before declaring first-class parity or publication complete:

1. Finish the owner-mediated Northsky authorization and proof-of-both linking for
   the selected authority DID. Do not infer account linking from its handle/email.
2. Complete Chrome and Swift CLI ATProto authentication, expiry, revocation,
   handle/PDS migration and linking acceptance; exercise actual Tangled public and
   private repositories, access denial, sync/change detection and metadata proposals
   using disposable writable fixtures rather than the read-only mirror.
3. Complete owner-reviewed canonical publication/migration and reconstruction,
   generated-client XRPC calls, cross-tenant/admin-denial checks and the mapped
   tenant-host MCP acceptance. Existing telemetry opt-in is not public-publication
   consent; review potentially sensitive metadata and feedback before publication.
4. Publish the reviewed 96 schema records under the authority account and
   independently verify every DNS/PDS CID. Published DNS delegations alone do not
   establish publicly resolvable schema records.
5. Retire the temporary operator deployment before removing its nonce rows, clean
   up only its exact recipient coordination prefix and task-created automation
   credentials, and retain the durable private recovery bundle. Record remaining
   acceptance evidence before closing PR #85 or declaring the rollout complete.

### Temporary migration access retired

After owner approval, deleted the unaliased operator deployment
`dpl_4WbPmZo6LhxqYrJHfGLpuipS3tAy` and removed only its task-created automation
protection bypass. Production remains unpaused and healthy; Vercel SSO protection
remains enabled. Private recovery material is retained. Chrome still shows the
Northsky password step, so authority linking and Lexicon publication remain pending.
