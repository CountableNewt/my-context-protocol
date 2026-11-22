# XRPC transport boundary

This package implements the ten currently typed draft contracts in `lexicons/`.
It does not mount production routes, issue tokens, validate OAuth proofs, or publish
schemas. Remaining inventory operations deliberately return `NotFound` until their
contracts and application adapters are implemented.

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
produce 204; others require a non-nil JSON result. Project lists use the declared
`projects` envelope; optional values in views omit SQL nulls. Return reviewed
`*Error` values for public errors; unexpected failures are sanitized. Rate limits
may include `RetryAfterSeconds`.

Input bounds, exact methods and scopes, duplicate and unknown properties, body/query
separation, and draft registry parity are tested. Output-schema validation and the
application adapter remain integration responsibilities. Keep inventory runtime and
publication flags pending until those integrations and public-resolution checks pass.
