#!/usr/bin/env bash
# Build the DispatchQ Windows x64 installer locally (cross-compiled from macOS or
# Linux). Windows artifacts are never built or published by CI; this is the only
# path that produces them.
#
# Output (default dist/windows/):
#   DispatchQ-<version>-windows-x64-setup.exe
#   SHA256SUMS
#
# Usage: scripts/build-windows.sh [output-dir]
#   DISPATCHQ_VERSION          version stamp (default dev-<branch>, like build-app.sh)
#   DISPATCHQ_WEBVIEW2=none    do not bundle the WebView2 bootstrapper (no download)
#
# Requires: go, Node.js 22+ (npm), a mingw-w64 cross compiler (the window uses
# cgo), makensis (NSIS 3), rsvg-convert and ImageMagick for the icon, curl.
# On macOS: brew install mingw-w64 makensis librsvg imagemagick
set -euo pipefail

export GOMAXPROCS="${GOMAXPROCS:-8}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/dist/windows}"
WORK="$ROOT/build/windows"
STAGE="$WORK/stage"
LOGO="$ROOT/web/public/logo.svg"

if [ -n "${DISPATCHQ_VERSION:-}" ]; then
  VERSION="${DISPATCHQ_VERSION#v}"
else
  BRANCH="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)"
  VERSION="dev-${BRANCH//\//-}"
fi
SETUP="DispatchQ-${VERSION}-windows-x64-setup.exe"

need() { command -v "$1" >/dev/null 2>&1 || { echo "build-windows: $1 not found ($2)" >&2; exit 1; }; }
need go "install Go"
need x86_64-w64-mingw32-gcc "brew install mingw-w64"
need x86_64-w64-mingw32-g++ "brew install mingw-w64"
need x86_64-w64-mingw32-windres "brew install mingw-w64"
need makensis "brew install makensis"
need rsvg-convert "brew install librsvg"
need magick "brew install imagemagick"
need curl "install curl"

rm -rf "$STAGE"
mkdir -p "$STAGE" "$OUT" "$WORK/cache"

echo "==> Building the web UI"
"$ROOT/scripts/build-web.sh"

echo "==> Rendering the icon"
ICONS="$WORK/icons"
rm -rf "$ICONS"; mkdir -p "$ICONS"
for px in 16 24 32 48; do
  rsvg-convert -w "$px" -h "$px" "$LOGO" -o "$ICONS/$px.png"
done
magick "$ICONS/16.png" "$ICONS/24.png" "$ICONS/32.png" "$ICONS/48.png" "$STAGE/dispatchq.ico"

echo "==> Building binaries (windows/amd64, version v${VERSION})"
LDFLAGS="-X github.com/danielmaier42/claudeq/internal/version.Version=v${VERSION}"
export GOOS=windows GOARCH=amd64
# The daemon and the window are GUI-subsystem programs: no console window opens at
# sign-in. The CLI keeps its console.
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$STAGE/dispatchqd.exe" "$ROOT/cmd/dispatchqd"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/dispatchq.exe" "$ROOT/cmd/dispatchq"
# The window uses the webview library, which is C++ and needs the cross compiler.
# Its icon is a Windows resource linked in through a .syso file next to the package.
SYSO="$ROOT/cmd/dispatchqapp/rsrc_windows_amd64.syso"
trap 'rm -f "$SYSO"' EXIT
printf '1 ICON "%s"\n' "$STAGE/dispatchq.ico" > "$WORK/app.rc"
x86_64-w64-mingw32-windres -i "$WORK/app.rc" -o "$SYSO"
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
  go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$STAGE/dispatchqapp.exe" "$ROOT/cmd/dispatchqapp"
rm -f "$SYSO"
unset GOOS GOARCH

cp "$ROOT/LICENSE" "$STAGE/LICENSE"
cp "$ROOT/scripts/windows/path-helper.ps1" "$STAGE/path-helper.ps1"

NSIS_DEFS=()
if [ "${DISPATCHQ_WEBVIEW2:-bundle}" != "none" ]; then
  BOOT="$WORK/cache/MicrosoftEdgeWebview2Setup.exe"
  if [ ! -s "$BOOT" ]; then
    echo "==> Downloading the WebView2 Evergreen bootstrapper"
    curl -fsSL "https://go.microsoft.com/fwlink/p/?LinkId=2124703" -o "$BOOT"
  fi
  cp "$BOOT" "$STAGE/webview2-bootstrapper.exe"
  NSIS_DEFS+=("-DBOOTSTRAPPER")
fi

echo "==> Building the installer"
makensis -V2 -INPUTCHARSET UTF8 "${NSIS_DEFS[@]}" \
  -DVERSION="$VERSION" -DSTAGE="$STAGE" -DOUTFILE="$OUT/$SETUP" \
  "$ROOT/scripts/windows/dispatchq.nsi"

( cd "$OUT" && shasum -a 256 "$SETUP" > SHA256SUMS )
echo "==> Done"
echo "    $OUT/$SETUP"
cat "$OUT/SHA256SUMS"
