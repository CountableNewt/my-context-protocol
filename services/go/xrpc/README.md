# XRPC transport boundary

This package implements typed contracts for every operation in
`lexicons/operations.json`. `scripts/generate-xrpc.py` generates Lexicon JSON,
embedded Go transport metadata and TypeScript bindings from reviewed typed views.
Run it with `--check` to reject stale artifacts. It does not issue tokens, validate
OAuth proofs, or publish schemas. Registry presence never installs an application
adapter or grants access.

Construct with `New(Config{Verifier: ..., Invoker: ...})`. `Verifier.Verify` must
validate credential signatures/proofs, issuer, expiry, replay protections, service
audience, DID/account linkage, and granted method permissions. The transport requires
an Authorization header and never authenticates through a browser session alone.
`Principal.Scopes[operation.Scope()]` is a normalized verified permission; `rpc:<NSID>`
is not an OAuth scope parser and must never be inferred from an unverified token.
Admin methods additionally require current admin authorization.

`Invoker.Invoke` receives the verified principal, exact registered operation, query
parameters, and direct Lexicon JSON input fields. It must enforce current account and
resource ownership, input semantics, tenant restrictions, rate limiting, and canonical
publication. The REST path template is descriptive and must not be interpolated into
an HTTP request without validating and escaping path identifiers. Reuse application
logic rather than manufacturing session cookies or bypassing authorization.

Return `Result{Value: typedOutput}` on success. Methods without a Lexicon output
produce 204; others require a non-nil JSON result. Lists use declared envelopes
(`projects`, `keys`, `releases`, `skills`, `logs`, or `accounts`). Optional SQL nulls
are omitted. Decimal measurements are base-10 strings because Lexicon does not
have a floating-point type. Return reviewed
`*Error` values for public errors; unexpected failures are sanitized. Rate limits
may include `RetryAfterSeconds`.

Input bounds, exact methods and scopes, nested types, duplicate and unknown properties,
body/query separation, and registry parity are tested. Output views are projected onto
their declared schema and checked for required fields/types without changing REST
values. The control adapter shares all control operations with REST, including current
account/admin and project ownership checks. Identity, repository and MCP invokers
retain their own protocol-specific boundaries. Keep public-publication flags pending
until authoritative public resolution passes.
