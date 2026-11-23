# Private ATProto OAuth foundation

The client uses pinned Indigo OAuth support for identity discovery, PAR, PKCE,
DPoP and confidential client assertions. `NewClient` requires an explicit P-256
key and exact collection permissions. Metadata includes public JWKS and must be
served at the configured client ID before authentication can work.

All discovery and resource clients reject private destinations, non-HTTPS URLs,
environment proxies and redirects. Socket checks also reject private addresses
after DNS resolution. No global HTTP defaults are changed.

`Store` implements the SDK's private persistence interface using encrypted Turso
rows. Authorization requests are create-only, expire after ten minutes and are
atomically consumed before callback exchange. Expired requests need bounded
`PruneRequests` maintenance. Revocation clears session secrets and retains a
hash-only tombstone so an in-flight refresh cannot recreate a logged-out session.

`SessionRecords` binds the canonical record client to the authenticated DID and
resolved PDS, including SDK nonce retries and refresh. Callers must coordinate
refreshes of the same persisted session across processes; the SDK only locks
within a single session object. This package does not yet supply that lease.

The identity service mounts browser-bound login and proof-of-both account linking
when `ATPROTO_ENABLED` is configured. `FlowClient` persists PAR state before
redirecting and exposes its state for the identity service's one-use browser
binding. The CLI opens that account initiation route in the browser, ensuring
the browser receives its binding cookie rather than stranding it in a CLI fetch.
ATProto-only accounts use a null GitHub ID; linking another identity never merges
accounts by handle or email. Live OAuth/PDS migration and Chrome acceptance still
need validation against configured hosted accounts.

`ServiceAuth` permissions specify exact DID service references (including their
`#service` fragment) and exact method NSIDs. They are serialized and validated by
the official SDK. No wildcard/transitional permissions are synthesized. Dynamic
Tangled audiences require an incremental authorization flow before requesting
new service-auth tokens.

Apply schema additions only with the explicit `cmd/migrate` operator command.
Never publish OAuth state, tokens, signing keys or session payloads as records.
