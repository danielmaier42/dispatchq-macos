#!/usr/bin/env bash
# Check a scripts/build-windows.sh result independently of the build, before CI
# uploads it or a release publishes it: the installer's exact file name, the
# machine type of every payload program and WSL helper, the version stamped
# into the programs, the source commit, the checksum and BUILD-INFO.json.
#
# Usage: scripts/windows/verify-build.sh ARCH VERSION SOURCE_SHA OUT_DIR
#   ARCH        amd64, x64 or arm64
#   VERSION     the version the build was stamped with, without a leading v
#   SOURCE_SHA  the commit the build must come from (clean tree)
#   OUT_DIR     the output directory given to build-windows.sh
# Requires: go, jq, shasum.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=SCRIPTDIR/arch.sh
. "$ROOT/scripts/windows/arch.sh"

[ $# -eq 4 ] || { echo "usage: verify-build.sh ARCH VERSION SOURCE_SHA OUT_DIR" >&2; exit 2; }
ARCH="$1" VERSION="$2" SOURCE_SHA="$3" OUT="$4"
windows_arch "$ARCH" || { echo "verify-build: unknown architecture '$ARCH'" >&2; exit 2; }
STAGE="$ROOT/build/windows/stage-$WIN_LABEL"
SETUP="$(windows_installer_name "$VERSION" "$WIN_LABEL")"

fail() { echo "verify-build: $*" >&2; exit 1; }

for exe in dispatchqapp dispatchqd dispatchq; do
  got="$(pe_machine "$STAGE/$exe.exe" || echo unreadable)"
  [ "$got" = "$WIN_MACHINE" ] || fail "$exe.exe has PE machine $got, expected $WIN_MACHINE"
  # -trimpath keeps -ldflags out of the Go build information, so the stamped
  # version is checked as the literal string it puts into the program. The
  # window does not link internal/version, so it carries none.
  if [ "$exe" != dispatchqapp ]; then
    LC_ALL=C grep -aqF "v$VERSION" "$STAGE/$exe.exe" || fail "$exe.exe is not stamped with version v$VERSION"
  fi
  # Go records the commit only when it builds from a plain clone (not from a
  # git worktree); when it is there it must match too. BUILD-INFO.json below
  # names the commit in every case.
  info="$(go version -m "$STAGE/$exe.exe")" || fail "$exe.exe carries no Go build information"
  if grep -qE "^[[:space:]]+build[[:space:]]+vcs.revision=" <<<"$info"; then
    grep -qE "^[[:space:]]+build[[:space:]]+vcs.revision=$SOURCE_SHA\$" <<<"$info" || fail "$exe.exe was not built from $SOURCE_SHA"
    grep -qE "^[[:space:]]+build[[:space:]]+vcs.modified=false\$" <<<"$info" || fail "$exe.exe was built from a modified tree"
  fi
  echo "$exe.exe: PE machine 0x$got, v$VERSION, $SOURCE_SHA"
done
for pair in amd64:003e arm64:00b7; do
  arch="${pair%%:*}" want="${pair##*:}"
  got="$(elf_machine "$STAGE/wsl/dispatchq-wsl-linux-$arch" || echo unreadable)"
  [ "$got" = "$want" ] || fail "dispatchq-wsl-linux-$arch has ELF machine $got, expected $want"
  echo "dispatchq-wsl-linux-$arch: ELF machine 0x$got"
done

[ -s "$OUT/$SETUP" ] || fail "missing $OUT/$SETUP"
# Nothing else may look like an installer: the release publishes every match.
others="$(cd "$OUT" && find . -maxdepth 1 -type f -name '*.exe' ! -name "$SETUP")"
[ -z "$others" ] || fail "unexpected installers next to $SETUP: $others"
got="$(pe_machine "$OUT/$SETUP" || echo unreadable)"
[ "$got" = 14c ] || fail "installer has PE machine $got, expected the x86 NSIS stub (14c)"
[ "$(awk '{print $2}' "$OUT/SHA256SUMS")" = "$SETUP" ] || fail "SHA256SUMS does not list exactly $SETUP"
(cd "$OUT" && shasum -a 256 -c SHA256SUMS) || fail "checksum mismatch"
jq -e --arg head "$SOURCE_SHA" --arg version "$VERSION" --arg target "windows/$WIN_GOARCH" \
  --arg machine "0x$WIN_MACHINE" --arg setup "$SETUP" \
  --arg sha "$(awk '{print $1}' "$OUT/SHA256SUMS")" \
  '.source_head == $head and .source_dirty == false and .version == $version
   and .target == $target and .payload_pe_machine == $machine and .installer == $setup
   and .sha256 == $sha' \
  "$OUT/BUILD-INFO.json" >/dev/null || { cat "$OUT/BUILD-INFO.json" >&2; fail "BUILD-INFO.json does not match this build"; }
echo "verified $OUT/$SETUP"
