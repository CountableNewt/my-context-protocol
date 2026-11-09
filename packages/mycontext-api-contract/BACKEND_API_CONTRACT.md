# Backend API Contract

This document describes what the MyContextProtocol frontend expects from the backend API. Use it when implementing or integrating the backend.

---

## Overview

- **Auth:** GitHub OAuth (no email/password). Session-based via cookies.
- **Transport:** REST over HTTP/HTTPS. All requests use `Content-Type: application/json` and `credentials: "include"` (cookies).
- **Base URL:** Configurable via `NEXT_PUBLIC_API_URL` (e.g. `http://localhost:8080`).

---

## CORS & Credentials

The frontend runs on a different origin (e.g. `http://localhost:3000`). The backend must:

1. **Allow the frontend origin** in CORS (e.g. `http://localhost:3000` in dev, production URL in prod).
2. **Allow credentials** (`Access-Control-Allow-Credentials: true`).
3. **Set cookies** with `SameSite` and `Secure` appropriate for your setup (e.g. `SameSite=Lax` for same-site in dev).

---

## Authentication (GitHub OAuth)

### 1. Initiate login

**Request**

```
GET /auth/github?return_to=<url>
```

- `return_to`: Full URL to redirect the user after successful login (e.g. `http://localhost:3000/` or `http://localhost:3000/projects`). If omitted, the backend uses `FRONTEND_URL` or `CORS_ORIGIN` + `/` when configured.
- When `FRONTEND_URL` / `CORS_ORIGIN` are set, `return_to` must match one of those origins (open-redirect protection).
- Optional `prompt=select_account` asks GitHub to show its account chooser. Other prompt values return `400 Bad Request`.
- No auth required.
- Backend redirects to GitHub OAuth with a **signed** `state` parameter (HMAC via `ENCRYPTION_KEY`) so the post-login redirect does not rely on in-memory sessions or sticky load balancers.
- Requires `GITHUB_OAUTH_REDIRECT_URI` and `ENCRYPTION_KEY` (32-byte base64) to be configured.

**Response:** 302 redirect to GitHub.

---

### 2. OAuth callback

**Request**

```
GET /auth/github/callback?code=<code>&state=<state>
```

- GitHub sends the user here after authorization.
- Backend exchanges `code` for tokens, creates/updates user, creates session.
- Backend redirects to the `return_to` URL encoded in signed `state` with `?auth_token=<one-time-token>`. If `state` is missing or invalid, the user is sent to `/login?error=...` when `FRONTEND_URL` / `CORS_ORIGIN` is set, otherwise `400`.

**Response:** 302 redirect to `return_to` with `auth_token`; session is finalized via `/auth/confirm`.

---

### CLI browser login

`GET /auth/cli/github?return_to=<url>` starts the same GitHub authorization
flow for native CLI clients. Unlike the web login route, it accepts only the exact
canonical shape `http://127.0.0.1:PORT/callback?state=NONCE`, with an explicit port
from 1 through 65535 and a 43–128 character base64url nonce. No other query
parameters, user information, fragments, or alternate loopback spellings are
accepted in the callback URL. Optional `prompt=select_account` is supported on
the initiation request. Web `/auth/github` origin restrictions remain unchanged.

The CLI must bind its listener before opening the initiation URL, verify its
nonce on callback, and exchange the returned `auth_token` using
`GET /auth/confirm?token=<one-time-token>`. With no `redirect` parameter, confirm
returns the user JSON and account session cookie. It also returns
`X-Context-Frontend-Origin` when a frontend origin is configured, so native
clients can supply the existing required `Origin` header on session-authenticated
mutations. The confirm JSON response has `Cache-Control: no-store`.

CLI users authenticate as accounts; tenant MCP API keys do not grant management
API access. See [`apps/context/README.md`](../../apps/context/README.md).

### 3. Current user (session check)

**Request**

```
GET /auth/me
```

- Requires valid session cookie.
- Used to check if user is logged in.

**Response:** `200 OK` with JSON body:

```json
{
  "id": "string",
  "email": "string | undefined",
  "login": "string | undefined",
  "avatar_url": "string | undefined"
}
```

- `401 Unauthorized` if not authenticated (frontend treats this as “logged out”).

---

### 4. Logout

**Request**

```
POST /auth/logout
```

- Requires valid session cookie.
- Invalidates the session.
- A request that loaded the session before logout cannot recreate it after deletion.

**Response:** `200 OK` or `204 No Content`. Body is ignored.

---

## Projects API

### List projects

**Request**

```
GET /projects
```

**Response:** Either:

- `Project[]` (array of projects), or
- `{ "projects": Project[] }`

---

### Get project

**Request**

```
GET /projects/:id
```

**Response:** `Project` object.

**Errors:** `404` if project not found.

---

### Create project

**Request**

```
POST /projects
Content-Type: application/json

{
  "name": "string",
  "slug": "string",
  "subdomain": "string"
}
```

**Response:** `Project` object (the created project).

---

### Get repo connection

**Request**

```
GET /projects/:id/repo-connection
```

**Response:** `RepoConnection` object if connected, or `404` if not connected.  
The frontend treats any error (including 404) as “no connection” and returns `null`.

---

### Connect repo

**Request**

```
POST /projects/:id/connect-repo
Content-Type: application/json

{
  "owner": "string",
  "repo": "string",
  "branch": "string"
}
```

**Response:** `RepoConnection` object.

- **`409 Conflict`** — Pro + webhooks + GitHub App env configured, but no installation id yet. JSON: `{ "reason": "github_app_install_required", "install_url": "..." }`. Navigate to `install_url`, complete install on GitHub (include the repo), then POST again.

---

### Trigger sync

**Request**

```
POST /projects/:id/sync
```

**Response:** `200 OK` or `204 No Content`. Body is ignored.

---

### List releases

**Request**

```
GET /projects/:id/releases
```

**Response:** Either:

- `Release[]`, or
- `{ "releases": Release[] }`

---

### Activate release

**Request**

```
POST /projects/:id/releases/:releaseId/activate
```

**Response:** `200 OK` or `204 No Content`. Body is ignored.

---

### List API keys

**Request**

```
GET /projects/:id/api-keys?include_revoked=<bool>
```

- `include_revoked` (optional): When `true`, the response includes keys whose `status` is not `active` (e.g. `revoked`). When omitted or `false`, only **active** keys are returned (default dashboard behavior).

**Response:** Either:

- `ApiKey[]`, or
- `{ "api_keys": ApiKey[] }`

---

### Create API key

**Request**

```
POST /projects/:id/api-keys
Content-Type: application/json

{
  "name": "string | optional"
}
```

- `name`: Optional dashboard label (may be omitted or empty).

**Response:** JSON body:

```json
{
  "id": "string",
  "key": "string",
  "prefix": "string",
  "name": "string | null | undefined"
}
```

- `id`: API key row id (rename/revoke paths).
- `key`: Full API key (shown once to the user).
- `prefix`: Key prefix for display (e.g. `mcp_abc123...`).
- `name`: Normalized name if one was provided.

---

### Rename API key

**Request**

```
PATCH /projects/:id/api-keys/:keyId
Content-Type: application/json

{
  "name": "string"
}
```

- Renaming is only allowed while the key is **active**. **`409 Conflict`** if the key is revoked.

**Response:** `ApiKey` object (same shape as list items).

---

### Revoke API key

**Request**

```
DELETE /projects/:id/api-keys/:keyId
```

- Soft-revokes the key: it remains stored with `status: "revoked"` but **must not** authenticate MCP requests.
- Idempotent: repeating `DELETE` on an already-revoked key still succeeds (`204`).
- **Browser clients** must send a valid `Origin` or `Referer` for this mutating request (same as other `POST`/`PATCH`/`DELETE` dashboard calls).

**Response:** `204 No Content`

---

### List request logs

**Request**

```
GET /projects/:id/request-logs?limit=<n>&offset=<n>
```

- `limit` (optional): Max number of logs.
- `offset` (optional): Pagination offset.

**Response:** Either:

- `RequestLog[]`, or
- `{ "logs": RequestLog[] }`

---

### Get custom domain status

**Request**

```
GET /projects/:id/custom-domain
```

**Response:** `CustomDomainStatus`

---

### Set custom domain

**Request**

```
POST /projects/:id/custom-domain
Content-Type: application/json

{
  "hostname": "mcp.example.com"
}
```

**Response:** `CustomDomainStatus`

- Ensures the domain exists on the configured Railway Gateway service before saving; saving resets project verification.
- Show the project ownership TXT record plus **all** `platform_dns_records` returned by Railway. Railway ownership TXT, routing CNAME, and certificate-validation records are requirements, not alternative routing options.
- Production fails closed with HTTP 503 when Railway provisioning is not configured. A provider provisioning failure also returns HTTP 503 without saving the hostname.

---

### Verify custom domain and TLS

**Request**

```
POST /projects/:id/custom-domain/verify
```

**Response:** `CustomDomainStatus`

- Verifies `TXT _mcp-verify.<hostname>` against `verification_token`.
- Falls back to the legacy TXT-at-hostname check for existing domains.
- Ensures the Railway domain exists after project TXT verification (also repairs domains saved before the Railway migration).
- Requires Railway ownership verification and propagated traffic-routing records before marking the project domain verified. TLS can still be pending; inspect `certificate_status` separately.
- Already-verified projects skip the project TXT check, but still refresh Railway checks. An unavailable provisioning configuration returns HTTP 503; failed provisioning returns HTTP 502; missing DNS requirements return HTTP 400.

---

## Type Definitions

Use these shapes for request/response bodies.

### User

```json
{
  "id": "string",
  "email": "string | undefined",
  "login": "string | undefined",
  "avatar_url": "string | undefined"
}
```

### Project

```json
{
  "id": "string",
  "account_id": "string",
  "name": "string",
  "slug": "string",
  "subdomain": "string",
  "created_at": "string",
  "custom_domain": "string | null | undefined",
  "custom_domain_verified_at": "string | null | undefined",
  "active_release_id": "string | null | undefined",
  "mcp_url": "string | null | undefined",
  "mcp_oauth_enabled": "boolean"
}
```

- `mcp_oauth_enabled`: `true` when the API has `MCP_OAUTH_ENABLED` set; the tenant MCP host then exposes OAuth discovery and token endpoints alongside API-key auth.

### CustomDomainStatus

```json
{
  "hostname": "string | null | undefined",
  "verified": "boolean",
  "verification_token": "string | null | undefined",
  "verification_record_name": "string | null | undefined",
  "instructions": "string | null | undefined",
  "ownership_verification_record_name": "string | null | undefined",
  "ownership_verification_record_value": "string | null | undefined",
  "platform_dns_records": [
    {
      "type": "string",
      "name": "string",
      "value": "string",
      "status": "string | null | undefined",
      "purpose": "string | null | undefined"
    }
  ],
  "fly_ownership_verification_record_name": "string | null | undefined",
  "fly_ownership_verification_record_value": "string | null | undefined",
  "fly_a_record_values": "string[] | null | undefined",
  "fly_aaaa_record_values": "string[] | null | undefined",
  "fly_cname_record_value": "string | null | undefined",
  "certificate_status": "not_configured | pending | issued | failed | unknown | null | undefined",
  "certificate_message": "string | null | undefined"
}
```

- `verification_record_name`: usually `_mcp-verify.<hostname>` while project ownership is pending.
- `ownership_verification_record_name` / `ownership_verification_record_value`: provider-neutral aliases for the **project** ownership record, not Railway's ownership TXT record.
- `platform_dns_records` is optional/nullable. Each row is a required Railway DNS record with its exact type, name, and value. The Gateway includes Railway's separate ownership token as a TXT row with purpose `OWNERSHIP_VERIFICATION`; routing rows use `DNS_RECORD_PURPOSE_TRAFFIC_ROUTE`.
- Per-record status `DNS_RECORD_STATUS_PROPAGATED` indicates a ready record. Other statuses, including absent or unknown statuses, must not be shown as verified. Do not derive current Railway DNS readiness from the persisted project `verified` flag.
- `certificate_status` is the normalized Railway edge certificate state, independent of project verification and routing DNS readiness.
- The `fly_*` fields are deprecated compatibility fields and are null/omitted on the Railway backend. Do not use them for new setup instructions.

### Project catalog (`GET /projects/:id/catalog`)

Dashboard-only aggregate of the active release MCP surface plus catalog markdown. Response includes the same `mcp_url` and `mcp_oauth_enabled` as `GET /projects/:id`, plus `catalog_markdown`, `tools`, `resources`, and `prompts`.

### RepoConnection

```json
{
  "project_id": "string",
  "provider": "string",
  "repo_owner": "string",
  "repo_name": "string",
  "default_branch": "string",
  "auth_type": "string",
  "webhook_id": "string | null | undefined"
}
```

### Release

```json
{
  "id": "string",
  "project_id": "string",
  "commit_sha": "string",
  "status": "pending | ready | failed",
  "created_at": "string",
  "error_summary": "string | null | undefined"
}
```

### ApiKey

```json
{
  "id": "string",
  "project_id": "string",
  "name": "string | null | undefined",
  "key_prefix": "string",
  "status": "string",
  "created_at": "string",
  "last_used_at": "string | null | undefined"
}
```

- `status`: Typically `active` or `revoked` (only `active` keys authenticate MCP).

### RequestLog

```json
{
  "id": "string",
  "project_id": "string",
  "release_id": "string | null | undefined",
  "timestamp": "string",
  "client_id": "string | null | undefined",
  "method": "string",
  "latency_ms": "number | null | undefined",
  "status": "number",
  "error_code": "string | null | undefined"
}
```

---

## Error Handling

- The frontend expects JSON error bodies when `Content-Type` is `application/json`.
- On non-2xx responses, the frontend throws an `ApiError` with `status` and optional `body`.
- `401` on `/auth/me` is treated as “not logged in” (no user).
- `404` on `/projects/:id/repo-connection` is treated as “no repo connected”.

---

## Endpoint Summary

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| GET | `/auth/github` | No | Start GitHub OAuth |
| GET | `/auth/github/callback` | No | OAuth callback |
| GET | `/auth/me` | Yes | Current user |
| POST | `/auth/logout` | Yes | Logout |
| GET | `/projects` | Yes | List projects |
| GET | `/projects/:id` | Yes | Get project |
| GET | `/projects/:id/catalog` | Yes | MCP catalog + markdown for dashboard |
| PATCH | `/projects/:id/catalog-markdown` | Yes | Custom `mycontext_catalog` body |
| POST | `/projects` | Yes | Create project |
| GET | `/projects/:id/repo-connection` | Yes | Get repo connection |
| POST | `/projects/:id/connect-repo` | Yes | Connect repo |
| POST | `/projects/:id/sync` | Yes | Trigger sync |
| GET | `/projects/:id/releases` | Yes | List releases |
| POST | `/projects/:id/releases/:releaseId/activate` | Yes | Activate release |
| GET | `/projects/:id/api-keys` | Yes | List API keys (`include_revoked` optional) |
| POST | `/projects/:id/api-keys` | Yes | Create API key |
| PATCH | `/projects/:id/api-keys/:keyId` | Yes | Rename API key (active only) |
| DELETE | `/projects/:id/api-keys/:keyId` | Yes | Revoke API key (soft) |
| GET | `/projects/:id/request-logs` | Yes | List request logs |
# Portable Skill Runtime (schema v1)

Projects expose four stable, colon-free runtime tools by default:

- `resolve_context` bootstraps a task with ordered active and suggested skills, provenance, conflicts, capability bindings, missing context, and a trace.
- `get_skill` retrieves one complete compiled skill by stable ID and optional version.
- `report_skill_feedback` persists version-specific evidence and returns an issue draft. It never reports an external side effect unless the harness performs one.
- `report_skill_usage` records one best-effort batch of explicit agent-reported use or conscious skipping at task completion when telemetry is enabled. Missing reports remain unknown.

The one-release compatibility aliases `mycontext_catalog`, `discover_skills`, and `list_capabilities` remain callable but are omitted from `tools/list`. Their legacy arguments and output wrappers are normalized through the canonical runtime handlers; they do not maintain separate discovery or scoring logic.

Tool arguments preserve native nested JSON. Canonical results include both `structuredContent` and an equivalent JSON text content item for clients that do not consume structured results. Invalid arguments and unknown tool names return JSON-RPC `-32602`; failures encountered while executing an accepted tool call return a successful MCP tool response with `isError: true`.

Generated per-skill tools are disabled by default, so the default `tools/list` result is exactly the four canonical tools. A project can temporarily opt into legacy compiled-tool listing and invocation by setting `legacy_compiled_tools_enabled: true` in `provider_preferences_json` through `PATCH /projects/:id/skill-runtime`. Canonical and alias names are reserved and suppress any colliding compiled capability even when this switch is enabled.

Runtime frontmatter supports `kind`, `scope`, `activation`, `enforcement`, `priority`, `requires`, `conflictsWith`, `version`, and `lifecycle`. Legacy skills remain retrievable but compile with `explicit` activation and structured clarification questions.

Runtime assignments are exact-target records: `target_type` must equal `scope`, and `target_id` must identify the organization, workspace, repository, or task being matched (`*` or `global` for global scope). Legacy unscoped rows remain database-compatible during rolling deployment but are not activated or returned by the dashboard API.

Dashboard APIs:

- `GET|PATCH /projects/:id/skill-runtime` reads or updates scoped assignments, semantic settings, provider preferences, feedback authorization, telemetry consent, and recent trace events.
- `PATCH /projects/:id/releases/:releaseId/compiled-skills/:compiledSkillId` accepts a `runtime` sidecar patch alongside existing MCP metadata.
- An `activation` object requires a valid `mode`; omitted `intents`, `events`, `tags`, and `examples` default to empty arrays. Supplied list fields must be arrays of strings. The dashboard preserves existing event, tag, and example lists when editing other runtime metadata.
- `POST /projects/:id/releases/:releaseId/compiled-skills/:compiledSkillId/writeback` creates a branch and draft GitHub pull request; it never pushes the default branch.

Detailed runtime telemetry is disabled by default, stores hashes and coarse events rather than prompts or source code, and is pruned using the configured retention period (30 days by default).


## Per-Skill Usage Analytics

`GET /projects/:id/skill-usage` requires dashboard authentication and project ownership. It uses retained database event aggregates independently of runtime settings and transport request-log sampling. The existing `/api/projects/:path*` browser rewrite exposes it through the application origin.

Query parameters:

| Parameter | Values / Default |
| --- | --- |
| `window` | `24h`, `7d` (default), `30d`; constrained by retention |
| `page` | One-based page, default `1` |
| `page_size` | Default `25`, maximum `100` |
| `sort` | `activity` (default), `skill_id`, `surfaced`, `instructions_delivered`, `agent_reported_used`, `agent_reported_skipped` |
| `direction` | `desc` (default), `asc` |
| `skill_id` | Optional exact skill ID for detail |

Response fields are `collection_enabled`, `retention_days`, `requested_window`, `effective_from`, `effective_to`, `reporting_coverage: "partial"`, `historical_measurements: "legacy_resolver_only"`, `page`, `page_size`, `total`, and `skills`. Timestamps are ISO 8601. Each skill includes `skill_id`, `name`, `is_current`, nullable `last_activity`, and `counts`. Counts contain `surfaced`, `instructions_delivered`, `supporting_file_read`, `resolver_selected`, `resolver_suggested`, `resolver_excluded`, `agent_reported_used`, `agent_reported_skipped`, and `legacy_resolver_selected`.

With `skill_id`, the row also includes `versions` (nullable `release_id`, `version`, `checksum`, and `counts`), `resolver_exclusion_reasons`, and `agent_skip_reasons` (each reason entry is `{reason, count}`). These arrays are empty in the list response. Current skills remain visible with zero counts, while historical skills remain visible while events are retained. Renamed IDs remain separate. Legacy resolver events retain unknown version attribution; generic request counts never reconstruct historical skill usage. Disabling collection stops new activity but does not hide retained history. Zero means no recorded activity, not evidence of no actual use.

### Agent Usage Reports

`report_skill_usage` accepts native JSON arguments:

```json
{
  "report_id": "stable-task-report-id",
  "trace_id": "optional-resolution-trace-uuid",
  "skills": [
    {"skill_id": "example", "version": "1.0.0", "outcome": "used"},
    {"skill_id": "another", "version": "2.0.0", "outcome": "skipped", "skip_reason": "not_relevant"}
  ]
}
```

`report_id` is 1–128 characters; each batch has 1–100 entries. Canonical `get_skill` responses and resolver active/suggested entries include `releaseId`; agents should copy it into the report entry’s `release_id` to preserve exact attribution across releases. Each entry requires `skill_id` (1–128), `version` (1–512), and `outcome` (`used` or `skipped`), with optional `release_id` UUID and `checksum` (1–128). Unknown fields are rejected; free-text explanations are not accepted. `skip_reason` is required only for `skipped`, with one of `not_relevant`, `redundant`, `instruction_conflict`, `missing_capability`, `task_changed`, or `other`.

The server validates skill references against project releases and any trace against the authenticated project/client. Client identity is server-derived from the API key or OAuth principal and client, never supplied by the agent. Stable report IDs deduplicate retries within this identity: an identical retry returns `already_recorded`, and conflicting reuse is rejected. The result is `{status: "recorded" | "already_recorded" | "collection_disabled", recorded_count: number}`. Persistence failures return an error rather than acknowledging storage.

Agents should submit one best-effort task-end batch only for actual use or consciously considered and declined skills. They must not enumerate all surfaced skills as skipped, expose private deliberation, or block task completion on reporting failure. Passive observed telemetry failures do not fail instruction delivery. Collection shares existing opt-in and retention controls (30 days by default); neither prompts nor source code, credentials, or reasoning transcripts are stored.
