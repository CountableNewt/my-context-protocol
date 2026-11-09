#!/bin/sh
# Embed build metadata so installation produces one relocatable executable.
set -eu
task_version=$1
case "$task_version" in
  ''|*[!a-zA-Z0-9._+-]*) printf '%s\n' 'VERSION must contain only letters, digits, dot, underscore, plus or hyphen.' >&2; exit 1 ;;
esac
task_version_file=Sources/myctx/Version.swift
task_version_backup=$(mktemp)
cp "$task_version_file" "$task_version_backup"
trap 'cp "$task_version_backup" "$task_version_file"; rm -f "$task_version_backup"' EXIT HUP INT TERM
printf 'enum BuildVersion {\n  static let value = "%s"\n}\n' "$task_version" > "$task_version_file"
${SWIFT:-swift} build ${SWIFT_FLAGS:-} -c release
task_build_bin=$(${SWIFT:-swift} build ${SWIFT_FLAGS:-} -c release --show-bin-path)
mkdir -p bin
cp "$task_build_bin/myctx" bin/myctx
