# Repository snapshot fetch core

`Fetcher.Fetch` downloads the GitHub tarball API response, retries a rejected
optional token once anonymously, and extracts a bounded snapshot into a temporary
directory. The caller must call `Outcome.Cleanup` after compilation. The HTTP
transport and temporary parent directory are injectable; production URLs and
credential-bearing redirect hosts cannot be overridden.

Defaults preserve the Swift limits: 150 MiB compressed, 500 MiB file contents,
20,000 archive entries, 5,000 discovered skills, a 60-second download timeout and a
30-second extraction timeout. `Limits` permits lower values for tests or tighter
serverless budgets. Extraction also bounds expanded tar metadata/padding.

Compared with the old system-tar implementation, validation occurs **before**
writing entries; symlinks, hardlinks, device files and duplicate file entries are
rejected outright. Hidden files count toward archive budgets but are omitted from
skill discovery. Skill discovery remains recursive and matches `SKILL.md`
case-sensitively. Multi-directory archives retain the extraction root; a single
visible directory becomes the repository root. Full trailing archive-directory
SHAs are retained when available.

This package provides fetch/extraction and a bounded commit-API SHA fallback. It does not yet connect the sync
pipeline, database writes, or HTTP webhook handlers.
