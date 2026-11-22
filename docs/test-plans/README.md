# Test Plans

| Surface | Local command | CI job |
|---|---|---|
| Full workspace | `bash scripts/ci.sh` | `Test and build` |
| Go services, snapshots and migrations | `bash scripts/check-go.sh` | `Go services` and `Test and build` |
| Web dashboard | `cd apps/web && bun run verify` | `Test and build` |
| Shared TypeScript packages | `bun run typecheck` | `Test and build` |
| Swift CLI | `make -C apps/context check test build` | `Test and build`; tests/build also on `Swift CLI Linux` |
| Branch preview | Push a feature branch | Vercel Preview |
| Production deployment | Merge to `main` after checks pass | Vercel Git integration |

CI uses `scripts/ci.sh` as the shared local/GitHub entrypoint. Go SQL tests run real SQLite statements through an isolated Python test driver. Snapshot tests verify exact typed-row preservation, interrupted transfer behavior and offline restoration. Private production data and credentials must never enter fixtures or ordinary previews.

Validate rendered browser changes in Chrome. Test tenant OAuth and MCP on the actual mapped tenant hostname, not a generic deployment URL.
