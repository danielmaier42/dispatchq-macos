#!/usr/bin/env bash
# Collect the release's installers from the downloaded build artifacts. Fails,
# and the release is not published, unless the artifacts hold exactly these
# three files, each matching the checksum its build recorded:
#   dispatchq-<version>-macos-<arm64|x64>.pkg
#   dispatchq-<version>-windows-arm64-beta.exe
#   dispatchq-<version>-windows-x64-beta.exe
#
# Usage: scripts/release-assets.sh VERSION MAC_ARCH ARTIFACTS_DIR RELEASE_DIR
#   MAC_ARCH is the architecture verify-pkg.sh checked (arm64 or x64).
#   ARTIFACTS_DIR holds one directory per build, each with its SHA256SUMS.
set -euo pipefail

[ $# -eq 4 ] || { echo "usage: release-assets.sh VERSION MAC_ARCH ARTIFACTS_DIR RELEASE_DIR" >&2; exit 2; }
VERSION="$1" MAC_ARCH="$2" ARTIFACTS="$3" RELEASE="$4"
fail() { echo "release-assets: $*" >&2; exit 1; }

[ -n "$VERSION" ] || fail "no version"
case "$MAC_ARCH" in arm64 | x64) ;; *) fail "unknown macOS architecture '$MAC_ARCH'" ;; esac
expected="$(printf '%s\n' \
  "dispatchq-$VERSION-macos-$MAC_ARCH.pkg" \
  "dispatchq-$VERSION-windows-arm64-beta.exe" \
  "dispatchq-$VERSION-windows-x64-beta.exe")"
got="$(find "$ARTIFACTS" -type f ! -name SHA256SUMS -exec basename {} \; | LC_ALL=C sort)"
if [ "$got" != "$expected" ]; then
  printf 'got:\n%s\nexpected:\n%s\n' "$got" "$expected" >&2
  fail "the builds did not produce exactly the expected installers"
fi
# Check coverage by installer, not by total line count: a duplicated checksum
# for one file must never stand in for another file's missing checksum.
while IFS= read -r name; do
  asset="$(find "$ARTIFACTS" -type f -name "$name")"
  list="$(dirname "$asset")/SHA256SUMS"
  [ -f "$list" ] || fail "missing checksum for $name"
  awk -v expected="$name" '
    NF != 2 || $2 != expected { bad = 1 }
    END { exit (bad || NR != 1) }
  ' "$list" || fail "SHA256SUMS must list exactly $name once"
  (cd "$(dirname "$list")" && shasum -a 256 -c SHA256SUMS) || fail "checksum mismatch in $list"
done <<<"$expected"
mkdir -p "$RELEASE"
while IFS= read -r name; do
  cp "$(find "$ARTIFACTS" -type f -name "$name")" "$RELEASE/$name"
done <<<"$expected"
ls -l "$RELEASE"
