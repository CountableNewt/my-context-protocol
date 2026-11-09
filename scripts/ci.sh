#!/usr/bin/env bash
# Shared local/GitHub CI entrypoint.
# Runs Swift CLI checks, then frontend checks (typecheck, lint, test, build) and gateway
# checks (test, release build) in parallel. Any failed check makes the script fail.
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

header "SQLite migration tests"
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -v

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
# Swift checks (parallel with frontend)
# ---------------------------------------------------------------------------

swift_checks() {
  local swift_root="$ROOT/services/mcp-gateway"
  cd "$swift_root"
  mkdir -p .build/ci-logs

  header "Swift version"
  swift --version | tee .build/ci-logs/swift-version.txt

  swift package clean
  swift package resolve
  rm -rf .build/index-build 2>/dev/null || true
  find .build -name build.db -delete 2>/dev/null || true

  header "Swift test"
  run_logged swift-test env \
    DATABASE_URL="" \
    SUPABASE_DB_URL="" \
    DISABLE_ADMIN_ANALYTICS_ROLLUP_SCHEDULER=1 \
    DISABLE_STRIPE_RECONCILIATION_SCHEDULER=1 \
    swift test --disable-automatic-resolution \
      --enable-swift-testing --disable-xctest \
      --no-parallel \
      -Xswiftc -warnings-as-errors

  header "Swift release build"
  run_logged swift-build \
    swift build -c release --product App -Xswiftc -warnings-as-errors
}

# ---------------------------------------------------------------------------
# Run both sides in parallel; abort everything if either fails
# ---------------------------------------------------------------------------

FRONTEND_PID=""
SWIFT_PID=""
FAILED=0

frontend_checks &
FRONTEND_PID=$!

swift_checks &
SWIFT_PID=$!

# Wait for both, capturing exit codes.
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
wait_pid "swift"    "$SWIFT_PID"

if [[ $FAILED -ne 0 ]]; then
  echo ""
  echo "CI FAILED — one or more jobs failed (see output above)"
  exit 1
fi

echo ""
echo "CI PASSED"
