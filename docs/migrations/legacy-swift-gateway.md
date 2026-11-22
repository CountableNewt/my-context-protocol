# Legacy Swift gateway recovery checks

Production acceptance of the Go services completed after the exact SQLite import: normal Chrome CLI authentication, GitHub repository reads, public MCP initialization, tool discovery, catalog and skill retrieval, plus custom-domain status reads. Preview acceptance also verified an actual GitHub-signed external push and its durable ready sync job. The Swift gateway source in `services/mcp-gateway` is retained as legacy recovery/reference code.

Default CI now checks the active Go services, Next.js frontend, Swift CLI and SQLite migration tooling. It no longer cleans, resolves, tests or release-builds the retired Vapor gateway on every run. The required GitHub check remains named **Test and build**.

To run the preserved legacy gateway checks manually from the repository root:

```sh
bash scripts/check-legacy-swift-gateway.sh
```

This command retains the former package clean/resolve, serial Swift Testing suite with warnings as errors, scheduler/database test environment, and release App build. It requires the same Swift toolchain/dependencies as the former CI checks and writes logs under `.ci-logs` and the gateway `.build/ci-logs`. It does not deploy or switch traffic. Recovery after Go production writes requires the documented verified data rollback procedure; passing this script alone does not make an old snapshot current.
