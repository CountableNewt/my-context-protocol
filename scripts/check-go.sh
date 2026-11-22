#!/usr/bin/env bash
# Shared local and GitHub checks for the Go services. Python is required because
# the integration suites execute real SQLite SQL through its standard library.
set -euo pipefail

task_repository_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$task_repository_root/services/go"

command -v python3 >/dev/null || {
  echo "python3 is required for the Go SQLite integration tests" >&2
  exit 1
}

task_unformatted="$(gofmt -l .)"
if [[ -n "$task_unformatted" ]]; then
  echo "Go files need formatting; run gofmt -w on these files:" >&2
  echo "$task_unformatted" >&2
  exit 1
fi

go vet ./...
go test -race ./...
go build ./...
