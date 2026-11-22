# Go MCP Runtime

`cmd/mcp` serves the existing MCP JSON-RPC surface, ping probes, and bounded SSE.
When `MCP_OAUTH_ENABLED` is set, it composes the OAuth service through explicit
host/project adapters. `SAAS_MCP_PATH` is canonical (`MCP_PATH` is an optional
fallback); the default is `/mcp`.

Implemented behavior includes:

- API-key and OAuth bearer authentication, active/expired/revoked checks, tenant
  credential binding, verified custom domains, and SaaS subdomain resolution.
- Version negotiation, transport/origin validation, exact-origin browser CORS,
  JSON-RPC errors, lifecycle notification acknowledgments, and version-dependent
  rich tool results.
- Active-release tools/resources/prompts and release-bound pagination cursors.
  Compiled tools remain opt-in, reserved runtime names cannot be shadowed, and
  colon-prefixed legacy names are rejected.
- `resolve_context`, `get_skill`, `report_skill_feedback`, `report_skill_usage`,
  and the hidden catalog/discovery compatibility aliases.
- Canonical policy scope and assignment matching, clarification exclusions,
  negative routing, current-skill activation, exact events, deterministic scoring,
  required capability fallbacks, conflicts, ordering, and inline instruction caps.
- Versioned package resources, text/binary file reads, safe paths and checksums,
  prompt context, provenance, and resource links for supporting files.
- Persistent, consent-gated usage observations; atomic report reservations and
  deduplication; historical version attribution and trace ownership checks;
  persisted feedback drafts that claim no external issue creation.

The runtime does not execute provider tools itself. Capability bindings describe
available harness tools, and feedback returns a draft for an authorized harness
to execute. Unavailable skills, versions, and files return explicit errors.

## Persistent Serverless State

Apply `mcp.MigrationSQL` through the explicit release migration command before
starting services. It creates catalog revisions, per-credential subscriptions,
and per-IP quota windows. Startup performs no DDL.

Control and sync mutations that affect an active catalog must increment
`mcp_catalog_revisions` in the same transaction. SSE polls that durable revision
and emits catalog notifications plus updates for the credential's persisted
resource subscriptions. Streams end after 25 seconds by default (50 maximum),
send heartbeats, and request reconnection. `Last-Event-ID` detects missed catalog
changes after reconnect; notifications coalesce revisions into a refresh signal.
A live connection retains its initial authorization until bounded reconnect.

Subscription limits are enforced atomically, including across instances. OAuth
and MCP ingress share persistent IP quotas. Only `VERCEL=1` enables trusting the
platform's `X-Vercel-Forwarded-For`; generic forwarding headers do not change the
quota key. See the [Vercel request-header contract](https://vercel.com/docs/headers/request-headers).
Explicit `MCP_TRUST_X_FORWARDED_HOST` remains required for host forwarding.
Maintenance must prune obsolete quota windows and abandoned subscriptions.

JSON-RPC request bodies are limited to 2 MiB, encoded responses to 4 MiB,
resolver requests to 65,536 bytes, inventories/current IDs to 1,000 entries, and
snapshot catalogs to 5,000 ready skills. A response exceeding the bound fails
without recording instructions as delivered. Package limits match the ingestion
contract: 128 files and 256 KiB per file. No per-request local database or durable
process memory is used.

## Deployment Acceptance Still Required

Unit and real-SQLite integration tests cover policy, transport, auth, catalog,
files, reporting, quotas, and SSE. These tests do not establish live Turso,
Vercel streaming/cold-start, custom-domain TLS, external MCP-client, or GitHub
OAuth acceptance. Compare existing production catalogs and usage reports during
cutover; production must keep a verified backup and rollback route until that
acceptance passes.

Generated branch preview URLs support API-key MCP calls when tenant-host binding
is disabled for that preview. Tenant OAuth still needs a project-bound hostname;
a generic preview hostname alone does not identify an OAuth issuer/project.
Register approved preview callback URLs and attach branch tenant aliases, or
implement and review a separate project-path issuer design before claiming full
OAuth preview parity. Do not weaken tenant checks to hide this setup requirement.

No optional semantic/embedding provider is called by the resolver: like the
current Swift resolver, it uses deterministic policy and keyword matching.
