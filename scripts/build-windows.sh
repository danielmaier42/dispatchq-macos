#!/usr/bin/env bash
# Build a DispatchQ Windows installer locally (cross-compiled from macOS or Linux)
# for x64 or ARM64. Windows artifacts are never built or published by CI; this is
# the only path that produces them.
#
# Output (default dist/windows/ for x64, dist/windows-arm64/ for ARM64):
#   DispatchQ-<version>-windows-<x64|arm64>-setup.exe
#   SHA256SUMS        checksum of the installer
#   BUILD-INFO.json   source commit, toolchain and payload architecture
#
# Usage: scripts/build-windows.sh [--arch amd64|x64|arm64] [output-dir]
#   --arch                     target architecture (default amd64; also
#                              DISPATCHQ_WINDOWS_ARCH). x64 is an alias of amd64.
#   DISPATCHQ_VERSION          version stamp (default dev-<branch>, like build-app.sh)
#   DISPATCHQ_WEBVIEW2=none    do not bundle the WebView2 bootstrapper (no download)
#   DISPATCHQ_LLVM_MINGW=DIR   ARM64 only: llvm-mingw installation to use. Without
#                              it the script uses aarch64-w64-mingw32-clang from PATH
#                              or downloads the pinned llvm-mingw release below into
#                              build/windows/cache (checksum verified, nothing is
#                              installed system-wide).
#
# Requires: go, Node.js (npm), makensis (NSIS 3), rsvg-convert and ImageMagick for
# the icon, curl. The window uses cgo and needs a C/C++ cross compiler:
#   x64:   mingw-w64 (x86_64-w64-mingw32-gcc/g++/windres)
#   ARM64: llvm-mingw (aarch64-w64-mingw32-clang/clang++/windres)
# On macOS: brew install mingw-w64 makensis librsvg imagemagick
set -euo pipefail

export GOMAXPROCS="${GOMAXPROCS:-8}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=windows/arch.sh
. "$ROOT/scripts/windows/arch.sh"

ARCH="${DISPATCHQ_WINDOWS_ARCH:-amd64}"
while [ $# -gt 0 ]; do
  case "$1" in
    --arch) [ $# -ge 2 ] || { echo "build-windows: --arch needs a value" >&2; exit 2; }; ARCH="$2"; shift 2 ;;
    --arch=*) ARCH="${1#--arch=}"; shift ;;
    -*) echo "build-windows: unknown option $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
windows_arch "$ARCH" || { echo "build-windows: unknown architecture '$ARCH' (use amd64, x64 or arm64)" >&2; exit 2; }

DEFAULT_OUT="$ROOT/dist/windows"
[ "$WIN_LABEL" = x64 ] || DEFAULT_OUT="$ROOT/dist/windows-$WIN_LABEL"
OUT="${1:-$DEFAULT_OUT}"
WORK="$ROOT/build/windows"
STAGE="$WORK/stage-$WIN_LABEL"
LOGO="$ROOT/web/public/logo.svg"

# Pinned llvm-mingw release for ARM64 (official macOS universal and Linux builds of
# https://github.com/mstorsjo/llvm-mingw). The checksums are the release's own.
LLVM_MINGW_RELEASE=20261104
LLVM_MINGW_SHA256_macos_universal=66cbd5178c4a71b0d473e68ede172f8eb7d15d023e67f39201d0a69e21f6bf3c

if [ -n "${DISPATCHQ_VERSION:-}" ]; then
  VERSION="${DISPATCHQ_VERSION#v}"
else
  BRANCH="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)"
  VERSION="dev-${BRANCH//\//-}"
fi
SETUP="DispatchQ-${VERSION}-windows-${WIN_LABEL}-setup.exe"

need() { command -v "$1" >/dev/null 2>&1 || { echo "build-windows: $1 not found ($2)" >&2; exit 1; }; }
need go "install Go"
need makensis "brew install makensis"
need rsvg-convert "brew install librsvg"
need magick "brew install imagemagick"
need curl "install curl"

mkdir -p "$WORK/cache"

# Resolves the C/C++ cross compiler and resource compiler into CC_W, CXX_W, WINDRES_W.
if [ "$WIN_GOARCH" = amd64 ]; then
  CC_W=x86_64-w64-mingw32-gcc CXX_W=x86_64-w64-mingw32-g++ WINDRES_W=x86_64-w64-mingw32-windres
  TOOLCHAIN="mingw-w64 ($("$CC_W" -dumpversion 2>/dev/null || echo missing))"
  for t in "$CC_W" "$CXX_W" "$WINDRES_W"; do need "$t" "brew install mingw-w64"; done
else
  LLVM_BIN=""
  if [ -n "${DISPATCHQ_LLVM_MINGW:-}" ]; then
    LLVM_BIN="$DISPATCHQ_LLVM_MINGW/bin"
  elif command -v "$WIN_TRIPLE-clang" >/dev/null 2>&1; then
    LLVM_BIN="$(dirname "$(command -v "$WIN_TRIPLE-clang")")"
  else
    case "$(uname -s)" in
      Darwin) PKG=macos-universal; SUM="$LLVM_MINGW_SHA256_macos_universal" ;;
      *) echo "build-windows: no ARM64 cross compiler; set DISPATCHQ_LLVM_MINGW to an llvm-mingw installation" >&2; exit 1 ;;
    esac
    NAME="llvm-mingw-$LLVM_MINGW_RELEASE-ucrt-$PKG"
    TOOLS="$WORK/cache/toolchains"
    if [ ! -x "$TOOLS/$NAME/bin/$WIN_TRIPLE-clang" ]; then
      echo "==> Downloading llvm-mingw $LLVM_MINGW_RELEASE"
      mkdir -p "$TOOLS/dl"
      curl -fsSL "https://github.com/mstorsjo/llvm-mingw/releases/download/$LLVM_MINGW_RELEASE/$NAME.tar.xz" -o "$TOOLS/dl/$NAME.tar.xz"
      echo "$SUM  $TOOLS/dl/$NAME.tar.xz" | shasum -a 256 -c - >/dev/null \
        || { echo "build-windows: llvm-mingw checksum mismatch" >&2; rm -f "$TOOLS/dl/$NAME.tar.xz"; exit 1; }
      rm -rf "${TOOLS:?}/$NAME"
      tar -xJf "$TOOLS/dl/$NAME.tar.xz" -C "$TOOLS"
    fi
    LLVM_BIN="$TOOLS/$NAME/bin"
  fi
  CC_W="$LLVM_BIN/$WIN_TRIPLE-clang" CXX_W="$LLVM_BIN/$WIN_TRIPLE-clang++" WINDRES_W="$LLVM_BIN/$WIN_TRIPLE-windres"
  for t in "$CC_W" "$CXX_W" "$WINDRES_W"; do
    [ -x "$t" ] || { echo "build-windows: $t not found (llvm-mingw)" >&2; exit 1; }
  done
  TOOLCHAIN="llvm-mingw ($("$CC_W" --version | head -n1))"
fi

rm -rf "$STAGE"
mkdir -p "$STAGE" "$OUT"
# makensis resolves a relative OutFile against the script's directory.
OUT="$(cd "$OUT" && pwd)"

echo "==> Building the web UI"
"$ROOT/scripts/build-web.sh"

echo "==> Rendering the icon"
ICONS="$WORK/icons"
rm -rf "$ICONS"; mkdir -p "$ICONS"
for px in 16 24 32 48; do
  rsvg-convert -w "$px" -h "$px" "$LOGO" -o "$ICONS/$px.png"
done
magick "$ICONS/16.png" "$ICONS/24.png" "$ICONS/32.png" "$ICONS/48.png" "$STAGE/dispatchq.ico"

echo "==> Building binaries (windows/$WIN_GOARCH, version v${VERSION})"
LDFLAGS="-X github.com/danielmaier42/claudeq/internal/version.Version=v${VERSION}"
export GOOS=windows GOARCH="$WIN_GOARCH"
# The daemon and the window are GUI-subsystem programs: no console window opens at
# sign-in. The CLI keeps its console.
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$STAGE/dispatchqd.exe" "$ROOT/cmd/dispatchqd"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/dispatchq.exe" "$ROOT/cmd/dispatchq"
# The window uses the webview library, which is C++ and needs the cross compiler.
# Its icon is a Windows resource linked in through a .syso file next to the package;
# the file name restricts it to this architecture.
SYSO="$ROOT/cmd/dispatchqapp/rsrc_windows_$WIN_GOARCH.syso"
trap 'rm -f "$SYSO"' EXIT
printf '1 ICON "%s"\n' "$STAGE/dispatchq.ico" > "$WORK/app-$WIN_LABEL.rc"
"$WINDRES_W" -i "$WORK/app-$WIN_LABEL.rc" -o "$SYSO"
CGO_ENABLED=1 CC="$CC_W" CXX="$CXX_W" \
  go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$STAGE/dispatchqapp.exe" "$ROOT/cmd/dispatchqapp"
rm -f "$SYSO"
unset GOOS GOARCH

# A payload for the wrong machine type would install but never start.
for exe in dispatchqapp dispatchqd dispatchq; do
  got="$(pe_machine "$STAGE/$exe.exe" || echo unreadable)"
  [ "$got" = "$WIN_MACHINE" ] || { echo "build-windows: $exe.exe has PE machine $got, expected $WIN_MACHINE" >&2; exit 1; }
done

# The Linux helper for providers that run in WSL. Both architectures ship in
# every installer: the distribution's processor decides which one runs, not the
# installer's (an x64 installer also runs on Windows on Arm, whose distributions
# are arm64). The daemon checks the ELF machine again before starting one.
mkdir -p "$STAGE/wsl"
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/wsl/dispatchq-wsl-linux-$arch" "$ROOT/cmd/dispatchq-wsl"
done
elf_machine() { # prints the e_machine of a 64-bit little-endian ELF file, in hex
  [ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ] || return 1
  [ "$(od -An -tx1 -j4 -N2 "$1" | tr -d ' \n')" = "0201" ] || return 1
  od -An -tx1 -j18 -N2 "$1" | awk '{print $2 $1}'
}
for pair in amd64:003e arm64:00b7; do
  arch="${pair%%:*}" want="${pair##*:}"
  got="$(elf_machine "$STAGE/wsl/dispatchq-wsl-linux-$arch" || echo unreadable)"
  [ "$got" = "$want" ] || { echo "build-windows: dispatchq-wsl-linux-$arch has ELF machine $got, expected $want" >&2; exit 1; }
done

cp "$ROOT/LICENSE" "$STAGE/LICENSE"
cp "$ROOT/scripts/windows/path-helper.ps1" "$STAGE/path-helper.ps1"

NSIS_DEFS=()
if [ "${DISPATCHQ_WEBVIEW2:-bundle}" != "none" ]; then
  # The Evergreen bootstrapper is an x86 program that installs the runtime matching
  # the machine, ARM64 included.
  BOOT="$WORK/cache/MicrosoftEdgeWebview2Setup.exe"
  if [ ! -s "$BOOT" ]; then
    echo "==> Downloading the WebView2 Evergreen bootstrapper"
    curl -fsSL "https://go.microsoft.com/fwlink/p/?LinkId=2124703" -o "$BOOT"
  fi
  cp "$BOOT" "$STAGE/webview2-bootstrapper.exe"
  NSIS_DEFS+=("-DBOOTSTRAPPER")
fi

echo "==> Building the installer"
makensis -V2 -INPUTCHARSET UTF8 ${NSIS_DEFS[@]+"${NSIS_DEFS[@]}"} \
  -DVERSION="$VERSION" -DARCH="$WIN_LABEL" -DSTAGE="$STAGE" -DOUTFILE="$OUT/$SETUP" \
  "$ROOT/scripts/windows/dispatchq.nsi"

( cd "$OUT" && shasum -a 256 "$SETUP" > SHA256SUMS )
SHA="$(cut -d' ' -f1 "$OUT/SHA256SUMS")"
HEAD="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
DIRTY=false
[ -z "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ] || DIRTY=true
GO_VERSION="$(go env GOVERSION)"
cat > "$OUT/BUILD-INFO.json" <<JSON
{
  "source_head": "$HEAD",
  "source_dirty": $DIRTY,
  "version": "$VERSION",
  "go": "${GO_VERSION#go}",
  "target": "windows/$WIN_GOARCH",
  "payload_pe_machine": "0x$WIN_MACHINE",
  "wsl_helpers_elf_machine": {"amd64": "0x3e", "arm64": "0xb7"},
  "installer_stub": "NSIS $(makensis -VERSION) x86 (32-bit)",
  "c_toolchain": "$TOOLCHAIN",
  "installer": "$SETUP",
  "sha256": "$SHA",
  "runtime_tested": false
}
JSON
echo "==> Done"
echo "    $OUT/$SETUP"
cat "$OUT/SHA256SUMS"
