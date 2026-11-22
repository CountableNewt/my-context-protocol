# Go sync service

`Service.SyncProject(ctx, projectID)` implements the control API adapter. The HTTP
service additionally exposes authenticated `POST /internal/sync`, internal job
status reads, and raw-body-signed `POST /webhooks/github` push handling. Internal
requests require `INTERNAL_SERVICE_TOKEN`; webhooks use the configured App secret
or the matching repository connection's webhook secret. Events for other branches
are verified and ignored. No payment tier is consulted.

Work runs **inline** under a 70-second context deadline. There is no detached
background goroutine or pretend queue acknowledgement. `sync_jobs` persists
idempotency and outcome; `sync_project_locks` prevents overlapping project syncs.
Expired 120-second leases allow redelivery to reclaim failed/interrupted jobs
with a new release. An in-progress duplicate returns HTTP 202 with its running
state; completed duplicates do not fetch or compile again. Multiple target
projects share the webhook request deadline; a timeout fails the request and a
redelivery skips already completed projects. This is bounded synchronous work,
not a durable queue or scheduler.

Apply `MigrationSQL` using the migration runner before starting the service. It
adds job/lock state and catalog revisions without rewriting the Swift schema.
Staged packages, ancillary files, compiled skills, canonical documents, routing
and capabilities are unreachable until the publication transaction changes
`projects.active_release_id`. That transaction compares the prior release,
checks the current lease and increments `mcp_catalog_revisions`. Failed stages
leave failed/inactive releases, preserving the previous active catalog.

Compilation includes YAML front matter (official yaml.v3), portable metadata,
source policies with checksum staleness, repository/project-scoped runtime
overrides, validation reports, resource/tool/prompt schemas, recursive package
files, and prior-release dashboard metadata carry-forward. Bodies remain fresh;
bounded line diffs and body-change counts record changes. Source policy and
runtime activation lists default to empty when omitted, while front-matter
activation intents can inherit `use_when`, matching the Swift distinction.
A 16 MiB retained compiled-content budget prevents unbounded catalog assembly;
archive, individual skill and ancillary package limits remain separately enforced.

Explicit remaining gaps:

- GitHub App installation tokens require `Config.InstallationToken`; absent
  providers fail with HTTP 501. The standalone entrypoint wires `githubapp.Service`
  using App credentials; missing App credentials never downgrade installation auth.
- If both the archive-directory SHA and GitHub commit-API fallback are unresolved,
  the revision is explicitly recorded as `unknown`.
- GitHub installation/connect/webhook provisioning and metadata writeback/draft
  PR creation are provided by the separate `githubapp` package. Repository
  disconnect is not yet implemented.
- Runtime clarification question wording/options are generic; canonical missing
  fields and conservative activation remain present.
- No automatic scheduler resumes abandoned work; GitHub redelivery or an explicit
  retry is required after lease expiry. Orphaned failed/staged releases are
  retained for diagnostics and need the normal retention process.

Tests use actual Swift baseline schema through Python SQLite and in-memory HTTP
fixtures, covering signatures, idempotency, leases, transactional publication,
metadata carry-forward, package blobs and override precedence. No tests require
external network access.
