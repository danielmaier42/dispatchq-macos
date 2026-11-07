# Target architecture settings for scripts/build-windows.sh. Sourced, not run.
#
# windows_arch NAME resolves NAME (amd64 or x64, arm64 or aarch64) and sets:
#   WIN_GOARCH  Go architecture (amd64, arm64)
#   WIN_LABEL   installer file name label and NSIS ARCH define (x64, arm64)
#   WIN_TRIPLE  mingw target triple of the C/C++ cross compiler
#   WIN_MACHINE PE machine type (hex) every payload executable must carry
# It returns 1 for an unknown name.
windows_arch() {
  case "$1" in
    amd64 | x64 | x86_64)
      WIN_GOARCH=amd64 WIN_LABEL=x64 WIN_TRIPLE=x86_64-w64-mingw32 WIN_MACHINE=8664 ;;
    arm64 | aarch64)
      WIN_GOARCH=arm64 WIN_LABEL=arm64 WIN_TRIPLE=aarch64-w64-mingw32 WIN_MACHINE=aa64 ;;
    *)
      return 1 ;;
  esac
}

# pe_machine FILE prints the PE machine type of FILE in hex (8664 x64, aa64 ARM64,
# 14c x86), read from the COFF header the e_lfanew field at offset 0x3c points to.
pe_machine() {
  local off
  off=$(od -An -tu4 -j60 -N4 "$1" 2>/dev/null | tr -d ' ')
  [ -n "$off" ] && [ "$(od -An -c -j"$off" -N2 "$1" 2>/dev/null | tr -d ' ')" = PE ] || return 1
  printf '%x\n' "$(od -An -tu2 -j$((off + 4)) -N2 "$1" | tr -d ' ')"
}
