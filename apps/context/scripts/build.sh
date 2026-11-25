#!/bin/sh
# Embed build metadata in one relocatable executable without modifying sources.
set -eu
task_version=$1
case "$task_version" in
  ''|*[!a-zA-Z0-9._+-]*) printf '%s\n' 'VERSION must contain only letters, digits, dot, underscore, plus or hyphen.' >&2; exit 1 ;;
esac
mkdir -p bin
CGO_ENABLED=0 ${GO:-go} build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$task_version" -o bin/myctx .
