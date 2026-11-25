# Verified private source cache

`Hydrator{DB, Fetch, MaxSkills}.Hydrate(account, project)` reconstructs the
manifest's target active release without changing its active pointer.
`HydrateRelease` supports explicit historical release views. `Fetch` receives a
verified local repository connection ID and an immutable 40/64-digit revision; it
must return a caller-owned temporary snapshot with the same resolved revision.
The hydrator cleans every returned snapshot when that request finishes.

The source repository must match that connection's recorded owner/name or native
Tangled repository DID. Revision, relative `SKILL.md` path, full-file SHA-256,
compiled source checksum, and package checksum must agree. Parent symlinks,
traversal, moving branches, absent provenance, and ambiguous connections fail
closed. Legacy rows missing immutable canonical provenance need a verified sync.

The hydrator adds only verified instructions to the existing canonical JSON,
preserving unknown metadata. Domain record serialization strips that generated
top-level `instructions` property, so Git bodies remain private derived caches.
Ancillary files use the compiler's bounded package scanner and idempotent private
upserts; nested skills and unsafe entries are excluded. Completed bodies are
skipped on retries. At most 25 packages are rebuilt per request by default;
unfinished work returns the domain engine's resumable pending marker.

Run under the same per-account lease as canonical projection/publication.
`domainrecords.Engine` controls activation after all required bodies are ready.
This package neither publishes records nor fabricates missing public metadata.
