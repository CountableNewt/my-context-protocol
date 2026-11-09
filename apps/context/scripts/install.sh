#!/bin/sh
# Install one executable without editing shell configuration.
set -eu
task_install_dir=$1
task_install_version=$2
case "$task_install_dir" in
  /*) ;;
  *) printf '%s\n' 'BINDIR must be an absolute path.' >&2; exit 1 ;;
esac
case ":$PATH:" in
  *":$task_install_dir:"*) ;;
  *) printf 'Cannot install: %s is not on PATH.\nAdd the destination to PATH, then rerun make install.\n' "$task_install_dir" >&2; exit 1 ;;
esac
sh scripts/build.sh "$task_install_version"
mkdir -p "$task_install_dir"
task_install_tmp=$(mktemp -d "$task_install_dir/.myctx-install.XXXXXX")
trap 'rm -rf "$task_install_tmp"' EXIT HUP INT TERM
cp bin/myctx "$task_install_tmp/myctx"
chmod 755 "$task_install_tmp/myctx"
mv -f "$task_install_tmp/myctx" "$task_install_dir/myctx"
printf 'Installed MyContextCLI at %s/myctx\n' "$task_install_dir"
