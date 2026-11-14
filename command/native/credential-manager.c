/* Private protocol: operation argv; service\0username\0UTF-8-blob\0 on stdin.
 * No diagnostics contain credential data. Build with Windows SDK + Advapi32. */
#define _CRT_SECURE_NO_WARNINGS
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <wincred.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <wchar.h>
#include <fcntl.h>
#include <io.h>

#if defined(ASSIGN_EXPECT_ARM64) && !defined(_M_ARM64)
#error Build requires native ARM64 compiler target
#endif
#if defined(ASSIGN_EXPECT_X64) && !defined(_M_X64)
#error Build requires native x64 compiler target
#endif
static int failure(DWORD error) {
    return error == ERROR_ACCESS_DENIED ? 12 : error == ERROR_NOT_FOUND ? 4 : 13;
}
static wchar_t *wide(const char *text) {
    int count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, text, -1, NULL, 0);
    if (!count) return NULL;
    wchar_t *result = calloc((size_t)count, sizeof(wchar_t));
    if (result && !MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, text, -1, result, count)) {
        free(result); return NULL;
    }
    return result;
}
int main(int argc, char **argv) {
    char input[65536];
    wchar_t *service = NULL, *user = NULL, *target = NULL;
    int status = 13;
    size_t size, offsets[3], parts = 0;
    if (argc != 2 || (strcmp(argv[1], "read") && strcmp(argv[1], "write") && strcmp(argv[1], "delete"))) return 13;
    _setmode(_fileno(stdin), _O_BINARY);
    _setmode(_fileno(stdout), _O_BINARY);
    size = fread(input, 1, sizeof(input), stdin);
    if (ferror(stdin) || size == sizeof(input) || !size || input[size-1] != 0) goto done;
    offsets[0] = 0;
    for (size_t i = 0; i < size; i++) if (input[i] == 0) {
        parts++;
        if (parts < 3) offsets[parts] = i+1;
    }
    if (parts != 3) goto done;
    service = wide(input); user = wide(input + offsets[1]);
    if (!service || !user || !*service || !*user) goto done;
    size_t length = wcslen(service) + wcslen(user) + 2;
    if (length > CRED_MAX_GENERIC_TARGET_NAME_LENGTH + 1) goto done;
    target = calloc(length, sizeof(wchar_t));
    if (!target) goto done;
    wcscpy(target, service); wcscat(target, L":"); wcscat(target, user);
    if (!strcmp(argv[1], "read")) {
        PCREDENTIALW item = NULL;
        if (!CredReadW(target, CRED_TYPE_GENERIC, 0, &item)) { status = failure(GetLastError()); goto done; }
        status = fwrite(item->CredentialBlob, 1, item->CredentialBlobSize, stdout) == item->CredentialBlobSize && fflush(stdout) == 0 ? 0 : 13;
        if (item->CredentialBlobSize) SecureZeroMemory(item->CredentialBlob, item->CredentialBlobSize);
        CredFree(item);
    } else if (!strcmp(argv[1], "write")) {
        size_t bytes = size - offsets[2] - 1;
        if (bytes > CRED_MAX_CREDENTIAL_BLOB_SIZE) goto done;
        CREDENTIALW item = {0};
        item.Type = CRED_TYPE_GENERIC;
        item.TargetName = target;
        item.UserName = user;
        item.Persist = CRED_PERSIST_LOCAL_MACHINE;
        item.CredentialBlobSize = (DWORD)bytes;
        item.CredentialBlob = (LPBYTE)(input + offsets[2]);
        status = CredWriteW(&item, 0) ? 0 : failure(GetLastError());
        if (status == 4) status = 13;
    } else {
        status = CredDeleteW(target, CRED_TYPE_GENERIC, 0) ? 0 : failure(GetLastError());
        if (status == 4) status = 0;
    }
done:
    SecureZeroMemory(input, sizeof(input));
    free(target); free(service); free(user);
    return status;
}
