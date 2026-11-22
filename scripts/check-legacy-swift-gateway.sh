#!/usr/bin/env bash
# Manual legacy gateway verification for recovery; excluded from active Go CI.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$ROOT/.ci-logs"
mkdir -p "$LOG_DIR"
header() { echo "==> $*"; }
run_logged() {
  local label="$1"; shift
  local log="$LOG_DIR/${label}.log"
  if "$@" > >(tee "$log") 2>&1; then
    return 0
  else
    local rc=$?
    echo "✗ [$label] FAILED (exit $rc) — log: $log"
    return "$rc"
  fi
}

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
    swift test --disable-automatic-resolution \
      --enable-swift-testing --disable-xctest \
      --no-parallel \
      -Xswiftc -warnings-as-errors

  header "Swift release build"
  run_logged swift-build \
    swift build -c release --product App -Xswiftc -warnings-as-errors
