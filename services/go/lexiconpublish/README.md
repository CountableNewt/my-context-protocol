# Lexicon publication and public verification

The selected authority is `samclemente.me`,
`did:plc:qy5pluw2bsuq2x6albsgkvx3`. Local schema validation and DNS manifest
generation need no credentials and perform no network writes:

```sh
go run ./cmd/lexicon --directory ../../lexicons \
  --expected-authority did:plc:qy5pluw2bsuq2x6albsgkvx3 --dry-run
```

Review the generated per-group TXT records. DNS changes are deliberately separate
from record publication. The publisher uses the existing linked account's
encrypted OAuth session and distributed refresh lease. That session must grant
create/update for `com.atproto.lexicon.schema`; reconnect with the reviewed scope
if necessary. Credentials come only from the existing environment/secret store.
Schema migrations must already have been applied explicitly.

```sh
go run ./cmd/lexicon --directory ../../lexicons \
  --expected-authority did:plc:qy5pluw2bsuq2x6albsgkvx3 \
  --account APPLICATION-ACCOUNT-UUID --publish

go run ./cmd/lexicon --directory ../../lexicons \
  --expected-authority did:plc:qy5pluw2bsuq2x6albsgkvx3 --verify
```

Every existing remote schema is checked for compatibility before the first write.
Existing definitions, types, required fields and constraints cannot be changed;
new optional object fields and definitions are permitted. Breaking changes require
another NSID. Writes use CID compare-and-swap and the durable publication journal;
interrupted writes recover using their original intent and CAS precondition.

A successful write produces **written_unverified**, never **verified**. Verification
resolves each group's `_lexicon` TXT record and independently reads the authority's
PDS without OAuth credentials. The public schema bytes and DAG-CBOR SHA-256 CID must
match the local reviewed artifact. Only that receipt establishes public resolution.
The command never changes operation readiness flags or deploys traffic.
