# Deployment and client configuration

Enable features only after the explicit schema migration and recovery rehearsal.
Use different assertion keys, database credentials and service audiences for
production and branch previews. A production DID audience must not authorize a
preview. Keep existing encryption/signing material; rotating it can invalidate
stored sessions and provider credentials.

| Variable | Purpose |
| --- | --- |
| `ATPROTO_ENABLED` | `true` enables OAuth, canonical records and scoped XRPC. Default disabled. |
| `ATPROTO_CLIENT_ID` | Public HTTPS `/auth/atproto/client-metadata.json` on this deployment. |
| `ATPROTO_CALLBACK_URL` | Same-origin HTTPS `/auth/atproto/callback`. |
| `ATPROTO_OAUTH_PRIVATE_KEY` | Private P-256 multibase assertion key, in the secret store only. |
| `ATPROTO_OAUTH_KEY_ID` | Stable key identifier matching public client metadata. |
| `ATPROTO_SERVICE_DID` | Deployment `did:web:<host>#mycontextprotocol` service reference. |
| `ATPROTO_RECORD_COLLECTIONS` | Comma-separated explicit writable collections. |
| `ATPROTO_SERVICE_PERMISSIONS` | JSON array of `{ "audience": "...", "method": "..." }` grants. |
| `TANGLED_ENABLED` | Explicitly enable authenticated Tangled capabilities. |
| `TANGLED_API_URL` | Verified Bobbin origin; current pinned service uses `https://api.tangled.org`. |
| `TANGLED_SERVICE_DID` | Verified Bobbin DID/service reference; currently `did:web:api.tangled.org#tangled_bobbin`. |
| `TANGLED_ARCHIVE_MIRROR_URL` | Explicit bounded archive mirror; pinned adapter uses `https://mirror-fsn.tangled.network`. |

The service DID document is served at `/.well-known/did.json`; its configured DID
must match the request host. OAuth client metadata is public and includes public
assertion keys, never private keys. The PDS must reach both metadata and callback
origins; deployment protection must not be bypassed by disabling security globally.
Use a properly configured public acceptance origin before asking the user to grant.

For normal product use, record collections are the 19 `dev.mycontextprotocol.data.*`
record types plus `org.tangled.graph.attestation`. Add
`com.atproto.lexicon.schema` only to the authority publisher's reviewed grant.
Record types can be enumerated from checked-in schemas; do not request `repo:*`.

Platform service permissions use exact methods from `lexicons/operations.json`
and this deployment's DID service audience. Include administrative methods only
for clients that need them; the server independently checks current admin status.
Tangled repositories have independently discovered DIDs, so the adapter permits
an audience wildcard only for its small reviewed list of exact Tangled methods.
See `atidentity.dynamicRepositoryMethod`; an arbitrary wildcard method is rejected.
Tangled/PDS credentials are never forwarded to GitHub, knots or caller-supplied URLs.

## External XRPC clients

Authenticate using ATProto OAuth with the needed explicit RPC permissions (or
published capability permission sets after their public resolution is verified).
Using the OAuth session at the user's current PDS, request
`com.atproto.server.getServiceAuth` for the exact deployment audience, exact method
and an expiration no more than 60 seconds ahead. Send the returned JWT to the
product service. Obtain a fresh token for every mutation; retries still use the
product operation's durable identity and may report pending publication.

```ts
import { XRPCClient } from "@mycontext/web-client";

const serviceOrigin = "https://mycontextprotocol.dev";
const audience = "did:web:mycontextprotocol.dev#mycontextprotocol";
const client = new XRPCClient(serviceOrigin, {
  async fetch(request) {
    const method = new URL(request.url).pathname.slice("/xrpc/".length);
    // pds is the caller's DPoP-bound ATProto OAuth client, never a browser cookie.
    const result = await pds.getServiceAuth({
      aud: audience,
      lxm: method,
      exp: Math.floor(Date.now() / 1000) + 60,
    });
    request.headers.set("authorization", `Bearer ${result.token}`);
    return fetch(request);
  },
});
const { projects } = await client.call(
  "dev.mycontextprotocol.project.listProjects", {},
);
```

`pds.getServiceAuth` above is an SDK adapter supplied by the integrating client;
it is not a method exported by this package. Generated operation inputs/outputs,
HTTP methods and owning services are exported from `@mycontext/web-client/xrpc`.
Queries use URL parameters; procedures use direct JSON fields. Decimal metrics
are strings. Optional SQL nulls are omitted from XRPC views. Errors have
`error` and `message`; 202 results explicitly represent incomplete publication.
The REST compatibility endpoints retain their existing response conventions.
Automatic MCP telemetry failures do not undo an already completed product action;
`X-Myctx-Telemetry-Publication: pending` or `failed` explicitly identifies the
separate publication state. Durable pending intents are resumed on later scoped
work. A missing header means no outstanding publication was reported.

Use the actual mapped tenant host for tenant MCP acceptance. A generic preview
URL verifies neither host mapping nor tenant OAuth. Published admin contracts,
public DNS delegation and valid service tokens never bypass ownership or admin
checks.
