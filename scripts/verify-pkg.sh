#!/usr/bin/env bash
# Check a scripts/build-pkg.sh result before a release publishes it: the exact
# file name, that every program in the app is built for exactly the named
# architecture and stamped with the version and source commit, and the bundle
# version. Writes SHA256SUMS for the package next to it.
#
# Usage: scripts/verify-pkg.sh ARCH VERSION SOURCE_SHA OUT_DIR
#   ARCH        arm64 or x64: the architecture in the file name
#   VERSION     the version the build was stamped with, without a leading v
#   SOURCE_SHA  the commit the build must come from (clean tree)
#   OUT_DIR     the output directory given to build-pkg.sh
# Requires: go, lipo, pkgutil, PlistBuddy, shasum (macOS).
set -euo pipefail

[ $# -eq 4 ] || { echo "usage: verify-pkg.sh ARCH VERSION SOURCE_SHA OUT_DIR" >&2; exit 2; }
ARCH="$1" VERSION="$2" SOURCE_SHA="$3" OUT="$4"
case "$ARCH" in
  arm64) MACHO=arm64 ;;
  x64) MACHO=x86_64 ;;
  *) echo "verify-pkg: unknown architecture '$ARCH' (use arm64 or x64)" >&2; exit 2 ;;
esac
PKG_NAME="dispatchq-$VERSION-macos-$ARCH.pkg"

fail() { echo "verify-pkg: $*" >&2; exit 1; }
VERSION_RE="$(printf '%s' "$VERSION" | sed 's/[][\\.*^$+?(){}|/]/\\&/g')"

[ -s "$OUT/$PKG_NAME" ] || fail "missing $OUT/$PKG_NAME"
# Nothing else may look like an installer: the release publishes every match.
others="$(cd "$OUT" && find . -maxdepth 1 -type f -name '*.pkg' ! -name "$PKG_NAME")"
[ -z "$others" ] || fail "unexpected packages next to $PKG_NAME: $others"

# The package is built from the working tree, which must be SOURCE_SHA, clean.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[ "$(git -C "$ROOT" rev-parse HEAD)" = "$SOURCE_SHA" ] || fail "the checkout is $(git -C "$ROOT" rev-parse HEAD), expected $SOURCE_SHA"
[ -z "$(git -C "$ROOT" status --porcelain)" ] || fail "the checkout has uncommitted changes"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
pkgutil --expand-full "$OUT/$PKG_NAME" "$WORK/x" >/dev/null
APP="$WORK/x/Payload/Applications/DispatchQ.app"
[ -d "$APP" ] || fail "the package holds no Applications/DispatchQ.app"

short="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP/Contents/Info.plist")"
[ "$short" = "$VERSION" ] || fail "bundle version is $short, expected $VERSION"

for exe in dispatchqapp dispatchqd dispatchq; do
  bin="$APP/Contents/MacOS/$exe"
  [ "$(lipo -archs "$bin")" = "$MACHO" ] || fail "$exe is $(lipo -archs "$bin" 2>&1), expected only $MACHO"
  info="$(go version -m "$bin")" || fail "$exe carries no Go build information"
  grep -qE "internal/version\.Version=v$VERSION_RE([ \"]|\$)" <<<"$info" || fail "$exe is not stamped with version v$VERSION"
  # Go records the commit only when it builds from a plain clone (not from a
  # git worktree); when it is there it must match the checked commit too.
  if grep -qE "^[[:space:]]+build[[:space:]]+vcs.revision=" <<<"$info"; then
    grep -qE "^[[:space:]]+build[[:space:]]+vcs.revision=$SOURCE_SHA\$" <<<"$info" || fail "$exe was not built from $SOURCE_SHA"
    grep -qE "^[[:space:]]+build[[:space:]]+vcs.modified=false\$" <<<"$info" || fail "$exe was built from a modified tree"
  fi
  echo "$exe: $(lipo -archs "$bin"), v$VERSION, $SOURCE_SHA"
done

(cd "$OUT" && shasum -a 256 "$PKG_NAME" > SHA256SUMS)
echo "verified $OUT/$PKG_NAME"
cat "$OUT/SHA256SUMS"
