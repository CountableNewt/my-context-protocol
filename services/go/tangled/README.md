# Tangled repository client

This package implements **public read/snapshot primitives**, not complete Tangled
provider parity. Do not enable Tangled as a generally available provider until
private authorization, repository discovery/ownership checks, automatic updates,
and metadata proposals pass integration acceptance.

## Verified upstream contract

Pinned upstream: `tangled.org/tangled.org/core` commit
`d09a20049c96cc6618ab7825485e04192e5a5840`.

- `repoident/knoturl.go` establishes repository DID identity and the
  `#tangled_knot` / `TangledKnot` DID service. This client requires its public
  HTTPS origin and deliberately rejects legacy/path-bearing service URLs.
- `lexicons/org/tangled/git/getBranch.json` and
  `knot2/crates/knot-xrpc/src/reads.rs::repo_branch` establish the `repo`, `name`
  query and `name`, `hash` response. The repository DID is distinct from its owner
  account DID and from the owner's `sh.tangled.repo` declaration record URI.
- `lexicons/org/tangled/git/getArchive.json`,
  `knot2/crates/knot-xrpc/src/lib.rs`, and `reads.rs::repo_archive` establish the
  `repo`, `ref`, `format`, `prefix` archive query. The client first resolves a
  branch, then requests its immutable 40/64-character Git object ID. It never
  downloads a moving branch after resolving provenance.
- `lexicons/org/tangled/review/submitPatch.json` requires an existing ticket,
  base/head Git object IDs, and source repository DID. A GitHub-style JSON file
  update is not a valid Tangled proposal. Proposal support requires a verified
  Git write/fork and ticket/patch workflow, and is **not implemented here**.

The consulted schemas are copied unchanged under `upstream/`, with their upstream
license. Generated knot documentation comments describe some Git methods as
unused/deprecated by its web app; the pinned knot2 router still registers the
implemented read routes. Compatibility with a deployed knot must be verified,
not inferred from this pin alone.

## Security and limits

The zero-value client uses socket-level public-address validation for DNS
rebinding protection, disables environment proxies and redirects, and uses the
same protected client for DID resolution and archive reads. Clients injected by
trusted Go callers are responsible for equivalent transport policy. Endpoint URL
validation and redirect rejection remain in force with injected clients.

Snapshot extraction reuses `reposync`'s compressed/expanded byte, entry, timeout,
path traversal, duplicate entry, symlink, and special-file protections. Failed
extraction removes partial data. The caller must clean up successful snapshots.

No GitHub token, PDS access token, or service-auth JWT is sent by this package.
Denied reads fail immediately without anonymous retries. Private repository
reads and their required authorization are **not yet supported or claimed**.
This package does not establish that an account owns a repository; account-level
connection code must prove access and ownership separately before persistence.

Tests use deterministic pinned-shape responses and safe archive fixtures. Live
public/private knot and proposal acceptance remain outstanding.
