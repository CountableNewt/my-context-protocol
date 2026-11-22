#!/usr/bin/env bash
# Shared local/GitHub CI entrypoint.
# Runs Swift CLI checks, then frontend and Go checks in parallel.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

export CI="${CI:-true}"
export NEXT_PUBLIC_APP_ENV="${NEXT_PUBLIC_APP_ENV:-test}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

LOG_DIR="$ROOT/.ci-logs"
mkdir -p "$LOG_DIR"

# Print a timestamped header line.
header() { echo "==> $*"; }

# Run a command, tee-ing stdout+stderr to a log file. On failure, dump the log
# and propagate the exit code so the parent process group can be killed.
run_logged() {
  local label="$1"; shift
  local log="$LOG_DIR/${label}.log"
  if "$@" > >(tee "$log") 2>&1; then
    return 0
  else
    local rc=$?
    echo ""
    echo "✗ [$label] FAILED (exit $rc) — full output above / log: $log"
    return $rc
  fi
}

# ---------------------------------------------------------------------------
# Install (serial — both sides need this done first)
# ---------------------------------------------------------------------------

header "Swift CLI format, test, and build"
make -C "$ROOT/apps/context" check test build

header "Bun workspace install"
bun install --frozen-lockfile

# ---------------------------------------------------------------------------
# Frontend checks (parallel sub-jobs)
# ---------------------------------------------------------------------------

frontend_checks() {
  header "Typecheck"
  run_logged typecheck bun run typecheck

  header "Lint"
  run_logged lint bun run lint

  header "Test"
  run_logged test bun run test

  header "Build"
  run_logged build bun run build
}

# ---------------------------------------------------------------------------
# Go checks (parallel with frontend)
# ---------------------------------------------------------------------------

go_checks() {
  header "Go format, vet, race tests, and build"
  run_logged go-checks bash "$ROOT/scripts/check-go.sh"
}

# ---------------------------------------------------------------------------
# Run independent checks in parallel and report every failure
# ---------------------------------------------------------------------------

FRONTEND_PID=""
GO_PID=""
FAILED=0

frontend_checks &
FRONTEND_PID=$!

go_checks &
GO_PID=$!

# Wait for both check groups, capturing exit codes.
wait_pid() {
  local name="$1" pid="$2"
  if wait "$pid"; then
    echo "✓ [$name] passed"
  else
    echo "✗ [$name] FAILED"
    FAILED=1
  fi
}

wait_pid "frontend" "$FRONTEND_PID"
wait_pid "go"       "$GO_PID"

if [[ $FAILED -ne 0 ]]; then
  echo ""
  echo "CI FAILED — one or more jobs failed (see output above)"
  exit 1
fi

echo ""
echo "CI PASSED"
