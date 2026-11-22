# ATProto contracts and delivery status

`lexicons/operations.json` inventories current product and administrative operations,
including MCP tool companions, owning services, authorization boundaries, and native
protocol exceptions. Next.js `/api` endpoints are adapters over these operations.
This inventory is manually reviewed against the imperative Go routers; checking it
cannot automatically prove that a newly added conditional route has been inventoried.

Each operation tracks **contract**, **runtime**, and **publication** independently.
A draft contract does not mean its endpoint is served or its schema publicly resolves.
Pending operations intentionally have no placeholder schema: an `unknown` payload
would conceal contract work still required. Do not publish these experimental drafts
as stable schemas before the typed interfaces and transport tests are reviewed.

The initial typed schemas cover project CRUD/activation, selected admin operations,
and logout. Project lists use an XRPC `{ projects: [...] }` envelope. Optional SQL
null fields are omitted from XRPC views while REST retains its existing behavior.
Admin lookup requires exactly one nonempty identifier, enforced by application logic
because Lexicon cannot express that cross-field constraint. Published schemas and
public routes never grant admin access by themselves.

Run `python3 scripts/check-lexicons.py` for local structural, reference, inventory,
and frontend route consistency checks. This checker is deliberately narrower than
the official Lexicon validator; the implementation must also validate with a protocol
SDK and run wire-parity tests before publication. Use `--require-complete` as the
release gate: it currently fails because runtime, contracts, and publication remain
in progress. Do not change readiness flags merely to pass this gate.

## Publication checklist

Selected authority and hosted acceptance account: `samclemente.me`,
`did:plc:qy5pluw2bsuq2x6albsgkvx3`. DNS handle resolution and the DID document's
`alsoKnownAs` were verified on 2026-11-22; its current PDS is
`https://northsky.social`. Re-resolve identity and PDS before publication rather
than treating this observed endpoint as permanent configuration. No authority
session, publication credentials or DNS changes are implied by this selection.

1. Finish typed contracts and generate client bindings; test every mapped operation.
2. Publish reviewed JSON as `com.atproto.lexicon.schema` records with record keys equal
   to the schema NSID, using a dedicated authority identity.
3. Configure each group independently, for example
   `_lexicon.project.mycontextprotocol.dev` for `dev.mycontextprotocol.project.*`.
   The TXT value is `did=<authority DID>`; no namespace hierarchy fallback applies.
4. Publish permission-set Lexicons only after the implemented scopes are verified.
5. Resolve every NSID through public DNS and retrieve its authority record, checking
   the schema and CID against the release artifact. Record this evidence before
   marking `publication: published`.

Credentials, key hashes, OAuth state, and admin flags are private infrastructure
state. XRPC operations managing them do not imply public PDS records containing them.
Public record migration requires explicit account authorization and publication review.

## Implementation waypoints

- Repository sync dispatches native providers explicitly and fails before claiming
  work when one is unavailable. GitHub account tokens, webhooks, installation
  updates and metadata proposals stay within the GitHub adapter.
- `services/go/atidentity` supplies the pinned official OAuth SDK client, hardened
  discovery, encrypted durable state, callback replay protection and PDS executor.
  Login, account linking, CLI handoffs and distributed refresh leases remain open.
- `services/go/atproto` supplies CID-conditional PDS writes and a SQL intent journal
  with owner-scoped bounded recovery. Domain record schemas, consent, projections,
  deletion and account migration are not connected to product writes yet.
- `services/go/xrpc` supplies reviewed draft transport contracts and a verifier /
  application-invoker boundary. It never derives permissions from browser cookies
  or unverified JWT claims. Production mounting requires a real scoped verifier.

Schema additions `006_atproto_private_oauth` and
`007_atproto_publication_intents` are registered in explicit migration tooling.
They are not applied at service startup. The pinned SDK requires Go 1.26 or newer;
CI reads the required toolchain version from `services/go/go.mod`.

No existing product data has been publicly migrated, no schema is claimed as
published, and no production traffic should switch based on these foundations.
Full acceptance still requires Tangled private access and proposal parity, all
55 operation contracts and adapters, account publication consent, canonical
domain projections, migration rehearsals, generated clients, public DNS/schema
resolution, and Chrome/tenant-host acceptance.

References: [Lexicon specification](https://atproto.com/specs/lexicon),
[OAuth specification](https://atproto.com/specs/oauth), and
[XRPC specification](https://atproto.com/specs/xrpc).
