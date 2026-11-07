__declspec(dllexport) long DllCanUnloadNow(void) { return 0; }
int __stdcall DllMainCRTStartup(void *h, unsigned long r, void *p) { return 1; }
