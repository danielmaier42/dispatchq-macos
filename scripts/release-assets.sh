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
sums=0
while IFS= read -r -d '' list; do
  (cd "$(dirname "$list")" && shasum -a 256 -c SHA256SUMS) || fail "checksum mismatch in $list"
  sums=$((sums + $(wc -l <"$list")))
done < <(find "$ARTIFACTS" -type f -name SHA256SUMS -print0)
[ "$sums" -eq 3 ] || fail "$sums checksums for 3 installers"
mkdir -p "$RELEASE"
while IFS= read -r name; do
  cp "$(find "$ARTIFACTS" -type f -name "$name")" "$RELEASE/$name"
done <<<"$expected"
ls -l "$RELEASE"
