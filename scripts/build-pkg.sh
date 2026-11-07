#!/usr/bin/env bash
# Build dispatchq-<version>-macos-<arm64|x64>.pkg — a macOS installer that drops DispatchQ.app into
# /Applications and (via postinstall) sets up the per-user LaunchAgent so the
# daemon starts at login.
#
# Unsigned by default. To sign + notarize (notarization-ready hooks below), set:
#   DISPATCHQ_SIGN_APP_ID   Developer ID Application: … (signs the .app)
#   DISPATCHQ_SIGN_PKG_ID   Developer ID Installer: …   (signs the .pkg)
#   DISPATCHQ_NOTARY_PROFILE  notarytool keychain profile name (staples the .pkg)
#
# Usage: scripts/build-pkg.sh [output-dir]   (default: dist/)
# Requires: go, rsvg-convert, iconutil, pkgbuild (all macOS tools).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/dist}"
IDENTIFIER="de.maierdaniel.dispatchq"

# An explicit DISPATCHQ_VERSION wins — the release workflow sets it from the
# pushed tag, so only that CI build carries a real version number. Every other
# build (local, or CI on a branch) is a dev build, versioned "dev-<branch>".
# build-app.sh honours the same variable, so the app bundle and the .pkg file
# name stay in lockstep.
if [ -n "${DISPATCHQ_VERSION:-}" ]; then
  VERSION="${DISPATCHQ_VERSION#v}"
else
  BRANCH="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)"
  VERSION="dev-${BRANCH//\//-}"
fi

echo "==> Building app bundle"
"$ROOT/scripts/build-app.sh" "$ROOT/build"
APP="$ROOT/build/DispatchQ.app"

# Optionally sign the app (notarization requires a hardened, signed app).
if [ -n "${DISPATCHQ_SIGN_APP_ID:-}" ]; then
  echo "==> Signing app with $DISPATCHQ_SIGN_APP_ID"
  codesign --deep --force --options runtime --timestamp \
    --sign "$DISPATCHQ_SIGN_APP_ID" "$APP"
fi

echo "==> Staging payload"
STAGE="$(mktemp -d)"
mkdir -p "$STAGE/Applications"
# ditto is the macOS-correct way to copy an app bundle. (pkgbuild encodes each
# executable's com.apple.provenance xattr as an AppleDouble entry in the payload
# — that is standard and the installer decodes it back to an xattr; no visible
# ._ files end up in /Applications.)
ditto "$APP" "$STAGE/Applications/DispatchQ.app"

echo "==> Building installer package"
mkdir -p "$OUT"
# The name follows the release asset pattern
# dispatchq-<version>-<platform>-<architecture>.<ending>. build-app.sh builds for
# the host's architecture only, so the name takes the architecture the built
# programs actually carry.
ARCHES="$(lipo -archs "$APP/Contents/MacOS/dispatchqd")"
for exe in dispatchqapp dispatchq; do
  [ "$(lipo -archs "$APP/Contents/MacOS/$exe")" = "$ARCHES" ] \
    || { echo "build-pkg: $exe is $(lipo -archs "$APP/Contents/MacOS/$exe"), dispatchqd is $ARCHES" >&2; exit 1; }
done
case "$ARCHES" in
  arm64) ARCH_LABEL=arm64 ;;
  x86_64) ARCH_LABEL=x64 ;;
  *) echo "build-pkg: unexpected architectures '$ARCHES'" >&2; exit 1 ;;
esac
PKG="$OUT/dispatchq-$VERSION-macos-$ARCH_LABEL.pkg"

# Disable bundle relocation. By default pkgbuild marks .app bundles relocatable,
# so if a copy of de.maierdaniel.dispatchq is already indexed by Spotlight the installer
# overwrites THAT copy instead of installing to /Applications. Force a fixed
# /Applications install by setting BundleIsRelocatable=false in a component plist.
#
# Also disable the version check. By default the installer only overwrites an
# existing bundle when the new CFBundleShortVersionString compares as newer,
# and "dev-main"/"dev-<branch>" never does against a real release like
# "0.9.0" (or against each other) — so a dev build would silently install its
# receipt while leaving the old binaries in place. Force the overwrite always.
COMPONENT_PLIST="$(mktemp -d)/component.plist"
pkgbuild --analyze --root "$STAGE" "$COMPONENT_PLIST" >/dev/null
# Newer pkgbuild versions leave out keys they consider default (macOS 27 drops
# BundleIsRelocatable), so delete-then-add instead of Set, which fails on a
# missing key.
for key in BundleIsRelocatable BundleIsVersionChecked; do
  /usr/libexec/PlistBuddy -c "Delete :0:$key" "$COMPONENT_PLIST" 2>/dev/null || true
  /usr/libexec/PlistBuddy -c "Add :0:$key bool false" "$COMPONENT_PLIST"
done

PKGARGS=(
  --root "$STAGE"
  --component-plist "$COMPONENT_PLIST"
  --identifier "$IDENTIFIER"
  --version "$VERSION"
  --install-location "/"
  --scripts "$ROOT/scripts/pkg"
)
# Sign the installer if a Developer ID Installer identity is provided.
if [ -n "${DISPATCHQ_SIGN_PKG_ID:-}" ]; then
  PKGARGS+=(--sign "$DISPATCHQ_SIGN_PKG_ID")
fi
pkgbuild "${PKGARGS[@]}" "$PKG"

# Notarize + staple if a notarytool profile is configured (no-op otherwise).
if [ -n "${DISPATCHQ_NOTARY_PROFILE:-}" ]; then
  echo "==> Notarizing (profile $DISPATCHQ_NOTARY_PROFILE)"
  xcrun notarytool submit "$PKG" --keychain-profile "$DISPATCHQ_NOTARY_PROFILE" --wait
  xcrun stapler staple "$PKG"
fi

rm -rf "$STAGE"
echo "==> Built $PKG (version $VERSION)"
