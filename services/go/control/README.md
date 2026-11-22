# Go control API

The control service uses the existing Swift-generated SQLite schema through Turso's stateless HTTP API. Every dashboard request authenticates `vapor-session` against `app_sessions` and loads its account. JSON requests are bounded to 4 MiB and skill bodies to the existing 1 MiB source limit. Every project route independently checks project ownership; mutating browser routes require an exact configured frontend Origin or Referer. Admin routes require the stored admin flag. Payment bypass flags have no authority.

Implemented directly:

- Projects: list, create without a plan limit, get, rename; legacy set-active restores all account projects.
- API keys: list, create with a one-time plaintext response and SHA-256 storage, rename active keys, idempotent soft revocation.
- Releases: list with computed metadata health, readiness-checked activation, validation reports, compiled skill listing and metadata/body/routing/runtime patches.
- Runtime settings: deterministic resolution controls, scoped assignments validated against the active ready catalog, bounded retained diagnostics.
- Request logs with current API key display labels; account/project dashboard summaries and all supported timeseries ranges; skill usage counts, version attribution and categorized reasons.
- Admin metrics, lookup, admin-only directory, flag updates with self-revocation protection, aggregate refresh and timeseries.

Adapters supplied by the composition root:

- `Config.Catalog`: the MCP service's dashboard catalog response.
- `Config.Domains`: Vercel domain provisioning and ownership verification.
- `Config.Sync`: repository sync, protected by a shared database rate limit (60 attempts/hour by default).
- `Config.ConnectRepository` and `Config.Writeback`: GitHub repository connection/webhook setup and validated sidecar draft PR creation.

An absent adapter returns 404 rather than a successful placeholder. The executable composes the MCP catalog, in-process durable sync and GitHub repository helpers. Vercel domains are enabled when provider credentials are configured. Project deletion is not exposed because the current Swift API has no deletion route. Billing endpoints are not exposed.

Catalog edits, release activation and runtime mutations update `mcp_catalog_revisions` within their write transaction. Apply the controlled baseline/additive migrations before starting the service; service startup never silently initializes or replaces a database. Manual sync additionally requires `mcp_rate_limits` from the MCP additive migration. Background cleanup should prune expired limiter windows.

Tests execute the actual SQL against the complete Swift-generated schema using the shared Python SQLite test driver. They cover authorization, CSRF, key secrecy/revocation, activation and revision semantics, metadata edits, runtime assignment validation, usage and dashboard aggregation, and admin protection. Live browser, real GitHub/Vercel provisioning and Turso acceptance are separate cutover checks.

## Daily maintenance

`GET /internal/maintenance` authenticates only the exact `Authorization: Bearer <CRON_SECRET>` header. It does not accept a browser session or start background work. Configure a secret of at least 16 characters and route the Vercel daily cron directly to this service. Responses are `no-store`; unauthorized requests are 401 and other methods are 405.

The handler has a 25-second request deadline. `MAINTENANCE_BATCH_ROWS` defaults to 250 and is capped at 1,000 per cleanup category. Expired OAuth/GitHub handoffs, stale revoked-principal subscriptions and abandoned operation leases are removed in chunks. Current safety quota windows are retained; old terminal sync jobs expire after 30 days. Running jobs and pending releases are marked failed only after their lease has been expired for five minutes. Source releases and compiled packages are retained.

Skill events and deduplication reports expire at each project's configured retention cutoff, including inactive projects and projects with telemetry disabled; missing settings use 30 days. Cleanup does not reinterpret disabled consent as permission to retain old history.

The daily rollup refreshes the overlapping latest 48 hours, preserving historic aggregates. `MAINTENANCE_ROLLUP_ROWS` defaults to 50,000 and is capped at 100,000 source logs. The transaction counts a bounded range before replacing aggregates and never commits a truncated sample. Over-budget windows return `rollup_deferred: true` and `more_work: true`; a category reaching its deletion limit also reports remaining work conservatively. A later authenticated invocation can drain the backlog. The manual admin rollup remains available for a deliberate historical rebuild.

Apply `control.MaintenanceSQL` through the controlled migration command to install its timestamp/expiry indexes. The service does not apply migrations on boot. External monitoring should inspect non-2xx responses and `more_work`; daily execution does not imply that an arbitrarily large backlog was fully processed.
