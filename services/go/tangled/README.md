# Tangled repository client

This package implements native public snapshots, authenticated Bobbin discovery
and private reads, a sync provider adapter, a pure-Go metadata Git branch writer,
and native ticket/patch submission. Hosted private and proposal acceptance remain
required before general availability.

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
  Git write/fork first. `CreateProposal` supplies the native ticket/patch sequence
  after a caller has verified the source commit was pushed; it does not pretend
  a JSON file update creates a Git commit.

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

No GitHub or PDS access token is sent by this package. `ServiceTokens` obtains a
fresh method-bound, short-lived service JWT using the linked actor's PDS. Knot
write tokens address the repository DID; Bobbin tokens address its configured
service DID. Denials never fall back to anonymous reads.

## Authenticated discovery and private reads

`LookupRepository` uses `org.tangled.temp.repo.getRepoByName`, validating the
manifest AT-URI, repository DID, declaration owner and name. Bobbin's pinned
`check_private_subject` and private-space index enforce viewer access; a denied
or hidden repository fails before a connection can be persisted. Authenticated
lookup proves read access and declaration ownership, not that the viewer has
write permission. `ListRepositories` supplies bounded public manifest pages;
the upstream list handler does not include private-space manifests, so private
repositories are selected by exact authenticated owner/name lookup.

`AuthenticatedSnapshot` resolves a branch through
`org.tangled.temp.git.getBranch` and requests its immutable archive through
`org.tangled.temp.git.getArchive`. Bobbin checks private access before returning
the archive redirect. The client follows only an exact operator-configured
mirror origin, exact XRPC path and unchanged query, using a separate anonymous
request. No service JWT or cookies cross that audience boundary. Configure and
verify the Bobbin DID/origin and archive mirror together; these are trusted
operator settings, never connection input. Unknown redirects fail closed.

`Provider` resolves native repository/owner/actor DIDs from a durable
`ConnectionResolver`, rather than reinterpreting generic GitHub owner/name
fields. Private bindings always use authenticated reads. `Changed` performs one
bounded head check; the service must persist its cursor and schedule bounded
sync work. No perpetual loop is started here.

## Native proposals

`WriteMetadataBranch` uses pinned go-git v5.19.3 low-level Smart HTTP sessions,
with a per-operation protected transport. It requires no external Git executable,
shell, checkout, secret file, or process-global protocol registration. It fetches
only the selected branch at depth one, checks the independently supplied base
commit, edits `.mycontext/skills.yaml` through the shared `skillpolicy` merger,
and creates a deterministic parented commit. Untouched source blobs are retained;
symlinks at the policy path are rejected. A persisted branch name and commit time
allow safe retry after a lost push acknowledgement. New branches use a zero-old
reference compare-and-swap, and push report status must explicitly confirm the
exact proposal branch. The default branch is never pushed or rewritten.

Git transport bounds one pack at 64 MiB, object count at 50,000, each decoded
object/delta target at 16 MiB and total expanded objects at 128 MiB. Pack headers
and delta target sizes are scanned before storage allocation; protocol-library
integrity verification follows. Response destinations, redirects, byte budgets
and request lifetime remain constrained. SHA-256 Git repositories remain readable
through snapshots but metadata writes currently require SHA-1 because the pinned
stable go-git object model uses 40-character object IDs. The writer rejects an
unsupported base explicitly.

`WriteMetadataBranch` retains a public Smart HTTP compatibility path.
`WriteAuthenticatedMetadataBranch`, used by providerhub for every Tangled
writeback, instead reads source metadata through actor-authorized Bobbin XRPC.
It obtains the exact base commit's tree hash with `temp.git.listCommits`, lists
root and policy directory entries, and obtains their OIDs with `temp.git.getEntry`.
Each reconstructed source tree must hash to the original tree ID, and the policy
blob must hash to its original OID. Untouched blobs, symlinks and Gitlinks are
preserved by hash without reading their contents. Up to 512 entries per affected
directory and eight concurrent entry requests are allowed. Missing, truncated,
lossy or altered source metadata fails before any external write. Only the four
new Git objects are sent over authenticated receive-pack; there is no anonymous
Git read or PDS-token forwarding for private writeback.

The OAuth service-token manager must support serialized concurrent token minting
for one actor. Required Bobbin method grants include `org.tangled.temp.git`
`getBranch`, `listCommits`, `getTree`, `getEntry`, `getBlob`, and `getArchive`.

`CreateProposal` creates an open `org.tangled.track.ticket` on the repository's
knot and calls `org.tangled.review.submitPatch` with existing base/head object
IDs and source repository identity. Callers must first finish the real Git write
and persist the resulting source ref. A failed patch submission returns the
ticket reference in `ProposalError`; callers can resume through `SubmitPatch`
without creating another ticket. A lost ticket-create response is ambiguous and
must be reconciled, never blindly retried. Caller-side durable operation tracking
remain integration work. `AttestProposal` supplies the PDS author-attestation
step through a durable caller-provided `Attestor`; knot responses retain optional
`x-tngl-change` object IDs. Publication failures retain both knot subject
references, so attestation retry does not duplicate tickets or patches.

Tests use deterministic pinned-shape responses, service-token audience checks,
denial and redirect credential isolation, declaration owner checks, native
binding resolution, ticket/patch wire validation and safe archive fixtures.
Live public/private knot and proposal acceptance remain outstanding.

### Deployed branch-route compatibility

A read-only production probe on 2026-11-22 found that Bobbin forwards `getBranch`
as an empty HTTP 404 for public legacy-knot repositories while
`org.tangled.temp.git.listBranches` returns their branch names and immutable OIDs.
The authenticated adapter uses the pinned `tempListBranches.json` contract only
after `getBranch` returns 404. It retains the same actor/service/repository and
never retries authentication denials anonymously. Pagination is capped at ten
100-branch pages with bounded cursors and response sizes. Configure the exact
`org.tangled.temp.git.listBranches` service-auth grant in addition to `getBranch`;
no wildcard method permission is required.
