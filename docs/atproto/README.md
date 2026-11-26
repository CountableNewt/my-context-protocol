# ATProto and Tangled integration

The dashboard and Go CLI support ATProto OAuth alongside GitHub. Accounts use
DIDs as identity; linking proves the existing session and a fresh ATProto grant.
Handles and PDS locations are resolved again when sessions are used. Existing
projects retain legacy storage until their owner explicitly reviews and publishes
them. Connecting a private repository never grants public-publication consent.

## Storage and repository behavior

`services/go/domainrecords` defines 19 public record families under
`dev.mycontextprotocol.data.*`. The public allowlist includes project configuration,
repository identity, imported frontmatter, policy/runtime overrides, assignments,
release provenance, validation, feedback and opted-in telemetry. It excludes source
bodies, credentials, sessions, API-key verification, security state, leases and
operational queues. Git remains authoritative for skill files. Source hydration
verifies immutable revision, path and checksums before activating rebuilt releases.
Unknown authored frontmatter survives import; generated instruction bodies stay
private. Free-text metadata and feedback still require careful publication review.

Immutable version records and bounded reference shards precede a CID-conditional
project manifest. That manifest is the visibility boundary. Native application SQL
is evaluated in a rolled-back transaction, saved as a private mutation intent, and
applied atomically with the canonical receipt only after PDS publication. Pending
operations are explicit and resumable. Concurrent external changes require an owner
review; superseded native intents are never silently replayed over external edits.
Turso is the rebuildable projection and private operational store. Bounded audits
check referenced records when the repository head changes, even if the project
manifest is unchanged. Missing or invalid records block normal reads and mutations
until an owner repairs the PDS records and completes explicit reconciliation.

Repository dispatch covers discovery, access, immutable revisions, bounded source
fetching, change detection and metadata proposals. GitHub remains its own adapter.
Tangled uses pinned upstream Lexicons, authenticated Bobbin private reads, repository
DIDs, authenticated Git writes and attested proposals. Repository names, knot/Bobbin
locations and handles are mutable resolution data. See
[the Tangled adapter](../../services/go/tangled/README.md) for the pinned upstream
revision, capability constraints and bounds. There is no perpetual hosted worker.

Public records can be replicated. PDS deletion and retention collection cannot
recall copies already held elsewhere. Existing telemetry consent is separate from
renewed consent to public telemetry. The storage interface does not assume Spaces
capabilities; adopting Spaces requires its own capability review and migration.

## XRPC and schemas

`lexicons/operations.json` inventories **62** product and administrative operations,
their owning services, authorization and native protocol exceptions. Every operation
has a typed query/procedure, generated TypeScript bindings and a service invoker.
REST and XRPC invoke the same application operations. Admin contracts retain admin
checks; project operations retain ownership checks even for administrators.

The **97** checked-in schemas include shared views, record families and capability
permission sets. Permission sets inherit the selected deployment audience, stay
within their namespace group and separate administrator operations. They do not
grant account privileges merely by being published. Until public resolution is
verified, OAuth uses explicit granular permissions rather than unresolved includes.

`/xrpc/{nsid}` dispatches through the existing Vercel services. External clients use
short-lived, exact-method PDS-issued service-auth JWTs for the deployment's DID
service reference. Browser cookies, Origin headers and raw PDS OAuth access tokens
are not API authorization. Mutation tokens are replay-protected. The Next gateway
uses generated destinations, omits cookies, bounds procedure bodies and supports
credential-free CORS preflight. Tenant MCP operations still require the actual
mapped tenant host and a valid tenant credential.

OAuth protocol routes, MCP transport, provider webhooks, health checks and internal
maintenance keep their native transports. The operation manifest explains these
exceptions and maps underlying product operations to XRPC where applicable.

```sh
python3 scripts/generate-xrpc.py --check
python3 scripts/check-lexicons.py
python3 scripts/check-lexicons_test.py
cd services/go
go run ./cmd/lexicon --dry-run --directory ../../lexicons \
  --expected-authority did:plc:qy5pluw2bsuq2x6albsgkvx3
```

The publisher validates the complete schema catalog with the pinned official SDK,
checks compatibility before writing, journals CID-conditional schema publication,
and independently verifies DNS, current DID/PDS and exact record CIDs. Official
`com.atproto.repo.strongRef` is embedded only as a validation dependency. CI also compares record and RPC schemas against the checked-in initial contract
baseline. Keep that baseline as history; breaking changes require a new NSID.
Permission sets have separate consent-policy review because their grants may
evolve within their authority namespace. Product
schemas are published as `com.atproto.lexicon.schema` records keyed by their NSID.

The chosen authority and hosted acceptance account is `samclemente.me`,
`did:plc:qy5pluw2bsuq2x6albsgkvx3`. Its observed PDS was `https://northsky.social`
on 2026-11-22; resolve it again before use. The dry run emits the complete DNS
manifest: each group requires its own `_lexicon.<group>.mycontextprotocol.dev` TXT
record with `did=did:plc:qy5pluw2bsuq2x6albsgkvx3`. There is no parent fallback.

After an explicitly linked authority grant, run `cmd/lexicon --publish --account
<uuid> --expected-authority <did>` and then `--verify` with the same reviewed
schema directory. Only independently verified public receipts justify changing
operation publication status. `scripts/check-lexicons.py --require-complete` is
the rollout gate; it intentionally fails while public publication is pending.

## Verify published records

In a project, open **Repo → ATProto records → Verify records**. The check reads
its published manifest, every referenced shard and metadata record, validates
identity/content/CIDs, and compares the reconstructed metadata with the current
project. Results include checked counts by collection and the first failing
record URI/CID. Collections unused by that project are not treated as missing.

Verification never publishes records or reconciles project state. An unpublished
project reports `not_published`; missing records, invalid records, local drift,
PDS unavailability and interrupted checks have distinct statuses. Only
`verified` means the complete check passed. The request is bounded to 55 seconds;
an interrupted check reports `incomplete`, never success, and does not retain a
partial verification checkpoint. Verification remains available from the error
view when a canonical audit blocks normal project reads.

```sh
myctx publication verify --project PROJECT_ID --timeout 60s
```

Native XRPC clients use `dev.mycontextprotocol.publication.verifyProject` and need
the corresponding method permission; its declaration is included in the generated
publication capability bundle. Existing cookie-based CLI sessions use the REST
compatibility route.

## Explicit migration and recovery

Service startup never migrates the database. `cmd/migrate` registers migrations
through `016_tangled_attestation_intents`, including the guarded nullable-GitHub
account rebuild. Pause and drain writers, retain a verified private current-schema
snapshot, apply explicitly, and verify the schema before enabling the feature.
Production and preview credentials and data must remain isolated.

`cmd/records` defaults to a dry-run inventory. Its private review file contains the
exact digest, warnings and stable legacy IDs. Publication requires the linked
account, project, reviewed digest and explicit public consent; telemetry needs its
own consent. Bounded pending runs resume with the same review. Read-only
verification compares validated owner records against the local public projection.
See [operator commands](../../services/go/cmd/records/README.md).

Current recovery uses `cmd/import export-remote --current-schema` after writers
are paused. Legacy Swift-shaped export refuses the nullable account schema or
ATProto state rather than silently losing identities and publication intents.
Restore rehearsal must compare typed values, projects, provenance, active releases
and telemetry attribution before switching reads. Preserve OAuth encryption and
signing material privately. Rollback after cutover must reconcile records written
since the snapshot; restoring SQL alone cannot undo PDS writes.

## Rollout evidence and remaining gates

Implementation is feature-gated. The 15 namespace DNS delegations are published
and independently verified at the authoritative Marque nameservers. No hosted
schema migration, product/schema-record publication or production read cutover has
run. See [the production checkpoint](production-rollout-2026-11-22.md). Local deterministic tests cover interrupted
publication, concurrency, lost acknowledgements, retention, external edits,
projection reconstruction, source hydration, owner isolation and transport parity.
The repository's required **Test and build** check remains unchanged.

Hosted acceptance remains required: Chrome dashboard and CLI OAuth/linking,
expiry/revocation/handle/PDS changes, actual Tangled public/private repositories and
metadata proposals, public DNS-backed schema resolution, generated-client calls,
and mapped tenant-host MCP. Chrome native control subsequently verified the existing production GitHub login
and completed DNS publication. ATProto browser acceptance remains pending.
Vercel keeps Sensitive credentials inside the hosted environment; the explicit
signed operator service is prepared to use them there. Deployment is currently
blocked by the Hobby 100-deployments/day limit, and the owner chose to wait for
reset rather than upgrade billing. Linear and the custom
MyContextProtocol endpoint were not available in the connected tool inventory.

References: [Lexicon](https://atproto.com/specs/lexicon),
[OAuth](https://atproto.com/specs/oauth), [XRPC](https://atproto.com/specs/xrpc),
[permissions](https://atproto.com/specs/permission).
