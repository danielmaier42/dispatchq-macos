Real PE fixtures for the client checks, built from `good.c` and `noexport.c`:

    F=(-shared -nostdlib -s -Os -Wl,--entry=DllMainCRTStartup)
    x86_64-w64-mingw32-gcc "${F[@]}" -o x64-good.dll good.c
    x86_64-w64-mingw32-gcc "${F[@]}" -o x64-noexport.dll noexport.c
    aarch64-w64-mingw32-clang "${F[@]}" -o arm64-good.dll good.c   # llvm-mingw 20261104
    x86_64-w64-mingw32-gcc -nostdlib -s -Os -Wl,--entry=DllMainCRTStartup -o x64-exe.exe good.c

`x64-exe.exe` exports the right name but is not a DLL. The tests derive the
truncated and empty cases from these files.
