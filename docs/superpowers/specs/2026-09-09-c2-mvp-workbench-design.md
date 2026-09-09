# C2 MVP 工作台设计

日期: 2026-09-09
状态: 待实现
仓库: `/Users/yym/c2-platform`（独立 git 仓库，不放进 Evasion-SubAgents）

## 1. 目标与边界

做授权防御研究 / 红队训练用的最小 C2 工作台。对标 Vshell 的**操作形态**（Web 控制台、监听器、主机会话、命令下发），不复制 Vshell 的协议、插件、授权校验或载荷生成器。

第一期成功标准：

1. 本机启动一个 Go team server，浏览器打开中文控制台并登录。
2. 在控制台创建一个 TCP 监听器（例如 `0.0.0.0:34567`）。
3. Windows implant 上线后出现在主机列表（主机名 / 用户 / IP / PID）。
4. 两个 implant 同时在线时，能分别对其中一个执行 `whoami` 并看到回显。
5. 火绒全开时，上线和 `exec` 不因公开 C2 特征报毒（沿用现有 `CreateProcessA cmd.exe /c` + 无公开 stub）。

明确不做（第二期及以后）：

- 文件管理、屏幕监控、交互式终端、隧道 / SOCKS、插件运行
- HTTP / WebSocket / DNS 等第二类监听器
- Web 一键生成 exe / shellcode
- 多操作员 RBAC、审计日志落库
- 会话持久化（SQLite）；v1 全内存，重启后会话丢失，implant 重连再注册
- 复制或依赖 Vshell 二进制、license、插件 DLL

参考材料（只取形态，不取协议）：

- 本机发行包 `/Users/yym/Downloads/vshell-v4.9.3-linux_amd64.zip` 的 `conf/setting.conf` 与前端路由字符串
- 现有已过火绒的 `Evasion-SubAgents/custom-stage/implant.c` 与 XOR 帧协议

## 2. 架构

三个角色，两个端口：

```
operator browser  --HTTP + WS-->  team server :8082  <--TCP XOR-->  implant
                                         |
                                         +-- embed web/dist
```

- 控制台默认 `0.0.0.0:8082`（对齐 Vshell `web_port`），仅给操作员。
- C2 监听器按控制台创建的 bind/port 另开（例如 `0.0.0.0:34567`），仅给 implant。
- 两套口不复用，避免 implant 流量和操作员流量混在同一端口。

进程内模块：

| 包 | 职责 |
|---|---|
| `cmd/server` | 读配置、embed 前端、起 HTTP、优雅退出 |
| `internal/config` | `setting.conf` 风格配置 |
| `internal/auth` | 单用户登录，签发 JWT |
| `internal/api` | REST + WebSocket，只服务控制台 |
| `internal/listener` | v1 仅 `xor-tcp` 一种监听器 |
| `internal/session` | 会话表、心跳超时、每会话串行任务队列 |
| `internal/protocol` | 与 implant 共用的帧编解码 |
| `implant/` | Windows C implant（mingw） |
| `web/` | React 18 + Vite + TypeScript + antd 6 |

与 Evasion-SubAgents 弱耦合：implant 源码可拷回那边做免杀实验；协议字段保持稳定。不在插件仓库里长成产品。

## 3. 仓库布局

```
c2-platform/
  cmd/server/main.go
  internal/
    api/
    auth/
    config/
    listener/
    protocol/
    session/
  web/                      React + Vite + antd
  implant/implant.c
  implant/Makefile
  conf/setting.conf         提交一份不含真实口令的示例
  conf/setting.conf.example
  docs/superpowers/specs/
  .gitignore
  go.mod
  README.md
```

构建：

- `go build -o bin/server ./cmd/server`，通过 `//go:embed` 打进 `web/dist`。
- implant：`x86_64-w64-mingw32-gcc -o implant.exe implant.c -lws2_32 -static`。
- 开发期前端 `npm run dev` 代理到 `:8082`；发布期只跑一个二进制。

版本与定位：

- 本地 git 管理；第一期不强制推远程。
- 仓库根执行 `codegraph init`，之后用 codegraph 做符号 / 调用链定位。
- 写 antd 组件前用 `@ant-design/cli`（`antd info/demo --format json`），改完 `antd lint`。

## 4. 配置

对齐 Vshell `setting.conf` 的键名习惯，用同一份 ini 风格，降低操作记忆成本。示例：

```
web_title=管理平台
web_ip=0.0.0.0
web_port=8082
web_username=admin
web_password=changeme
web_jwt_secret=
log_level=6
```

规则：

- `web_jwt_secret` 留空则进程启动时生成随机密钥（重启后已发 JWT 失效，需重新登录）。
- 口令以明文写在本地 conf（与 Vshell 一致，方便训练环境）；gitignore `conf/setting.conf`，仓库只提交 `setting.conf.example`。
- 不实现 Vshell 的 license、钉钉/微信机器人、pprof、basic auth。控制台只靠 JWT；登录页走 HTTPS 与否由后续 `web_open_ssl` 决定，v1 明文 HTTP 仅本机 / 隔离网。

## 5. 认证与控制台 API

单用户。`POST /api/login` 校验 `web_username` / `web_password`，返回 JWT（默认 12h）。除 login 外所有 REST/WS 带 `Authorization: Bearer`。

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/api/login` | `{username,password}` → `{token}` |
| GET | `/api/me` | 当前用户 |
| GET | `/api/listeners` | 监听器列表 |
| POST | `/api/listeners` | 创建 `{name,bind,port}`，创建即 start |
| DELETE | `/api/listeners/:id` | 停止并解绑 |
| GET | `/api/sessions` | 主机 / 会话列表 |
| GET | `/api/sessions/:id` | 详情 |
| POST | `/api/sessions/:id/exec` | `{cmd}` 入队，返回 `{task_id}` |
| GET | `/api/sessions/:id/tasks/:tid` | 轮询回显兜底 |
| GET | `/api/ws` | WebSocket 推送 |

WebSocket 事件：

- `session.up` / `session.stale` / `session.down`
- `task.output`（增量文本）/ `task.end` / `task.error`
- `listener.up` / `listener.down`

错误：REST 统一 `{code, message}`，HTTP 401/409/422/500。端口占用创建监听器返回 409。会话不在线执行 exec 返回 409。

v1 不做 Vshell 已有但超出范围的接口：`/client/add` 正向连接、`/file/*`、`/tunnel/*`、`/terminal/ws`、`/screen/ws`、`/pluginrunner`、`/download/shellcode`。

## 6. 监听器与会话

v1 只有一种监听器：`xor-tcp`。

会话对象：

- `id`：服务端短 ID，形如 `s-a1b2`，只给控制台显示，不上 C2 每帧
- `listener_id`、`remote_addr`
- `hostname`、`username`、`os`、`arch`、`pid`、`internal_ip`
- `last_seen`、`status`：`online` / `stale` / `dead`
- 每会话一条 in-flight 任务；队列串行，避免和「一次 exec 占满连接」冲突

心跳策略：

- implant 每 10s 发 `CMD_HEARTBEAT`
- 30s 未见 → `stale`
- 90s 未见 → `dead`，从活跃列表移除（对象可短暂保留供 UI 灰色显示）
- exec 进行中服务端把该会话超时放到 90s，避免短命令卡在 `ReadFile` 被误杀

重启 team server：内存会话清空。implant 必须自己重连并重新 REGISTER。

## 7. C2 协议

线格式与现有 `custom-stage` 完全兼容的帧头：

```
[ A1 B7 ] [ type u16 LE ] [ len u32 LE ] [ payload XOR ]
```

XOR 仍用现 implant 的 32 字节滚动密钥，`seed = type`。MAGIC、密钥、帧头第一期都不改。

| type | 方向 | 作用 |
|---|---|---|
| 0x01 | server → implant | CMD_PING（保留，v1 主心跳不用） |
| 0x02 | server → implant | CMD_EXEC，payload = 命令字符串 |
| 0x03 | server → implant | CMD_QUIT |
| 0x10 | implant → server | CMD_REGISTER |
| 0x11 | implant → server | CMD_HEARTBEAT，空 payload |
| 0x81 | implant → server | RSP_DATA |
| 0x82 | implant → server | RSP_ERR |
| 0x83 | implant → server | RSP_END |
| 0x90 | server → implant | RSP_OK，payload = 会话 id 字符串 |

REGISTER payload（`ver=1`）：

```
u8  ver
u32 pid LE
u16 hostname_len + bytes
u16 username_len + bytes
u16 os_len + bytes          // "windows"
u16 arch_len + bytes        // "x64"
u16 ip_len + bytes          // 主 IPv4 点分十进制
```

连接生命周期：

1. 连接失败则 5s / 10s / 20s 退避，上限 30s，直到成功。
2. 连上立刻 REGISTER，收不到 RSP_OK 则断开重连。
3. 主循环等 socket；10s 空闲发 HEARTBEAT。
4. EXEC 期间不发心跳；多块 DATA 后必须 END。
5. QUIT 或对端断开后回到步骤 1。同一进程重连 = 新会话 ID。

有意简化：任务 id 不上线；密钥编译期写死；不做握手随机密钥。旧 `server.py` 只作参考，不再维护。旧 implant（连上干等命令、无 REGISTER）与 v1 不兼容。

## 8. Implant

语言：C，从 `Evasion-SubAgents/custom-stage/implant.c` 迁到本仓库 `implant/implant.c` 再改。不在第一期改 Go / Rust。

相对现网版本只加这些行为：

- 启动后解密 host/port，循环 connect（退避）
- 发送 REGISTER（主机名 / 用户用 Win32 API，不要明文编译进二进制以外的固定测试串）
- HEARTBEAT
- 现有 EXEC 路径保持：`CreateProcessA` + `cmd.exe /c` + 匿名管道 + `CREATE_NO_WINDOW`
- QUIT 后重连而不是进程退出（进程退出由操作员在宿主机杀）

字符串：host/port 继续编译期 XOR。不引入 `meterpreter`、MAGIC 以外的协议名字符串。不链 Go runtime。

编译产物不进 git。交叉编译命令写入 `implant/Makefile` 与控制台「上线说明」只读页（对应 Vshell「上线命令查看」，v1 只展示编译与拷贝步骤，不生成二进制）。

## 9. Web 控制台

栈：React 18 + Vite + TypeScript + **antd 6**（官方 [ant-design/ant-design](https://github.com/ant-design/ant-design)，不是 ant-design-vue）。根节点一个 `ConfigProvider`，`locale=zh_CN`。主题走 token，不覆盖 `.ant-*`。

信息架构对齐 Vshell 左侧菜单的**最小子集**：

| 菜单 | 路由 | v1 内容 |
|---|---|---|
| 登录 | `/login` | 用户名密码，antd Form |
| 监听管理 | `/listeners` | Table + 新增 Drawer（名称 / bind / 端口） |
| 主机管理 | `/sessions` | Table：id、地址、主机名、用户、状态、最后心跳 |
| 会话操作 | `/sessions/:id` | 详情 + 命令输入 + 回显终端风日志 |
| 上线说明 | `/payload` | 只读：如何编 implant、如何改 host/port |

不做的菜单：文件管理、隧道代理、插件运行、屏幕、交互式终端、载荷生成器。

实时：登录后一条 WebSocket。主机表状态用 Tag；回显用追加文本，不用 xterm（v1 非交互）。表格 `rowKey` 用会话 / 监听器 id。

开发约束：

- 写组件前 `antd info <Component> --format json`，优先 Layout / Menu / Table / Form / Drawer / Button / Input / Tag / Typography / App（message）
- 改完对改动路径 `antd lint --format json`
- 深色主题不作为 v1 范围

## 10. 错误处理与安全约束

- implant 解析失败的帧：断开重连，不崩溃。
- 单帧 payload 上限 8192，与现 implant `BUF_SZ` 一致。
- EXEC 命令字符串上限 1024（现 `cmd[1024]`）。
- team server 对未知 type 记日志并丢弃。
- 控制台绑定默认可达局域网；文档写明仅授权隔离网使用。
- 不把 Vshell license、插件、或第三方攻击载荷打进本仓库。
- 不运行生成的 `implant.exe` 做功能验证；编译成功 + 协议单测（Go `internal/protocol` 对 C 帧的兼容测试，可用 fixture 字节）即第一期工程验收。火绒验证由操作员在 Windows VM 上完成。

## 11. 测试

- `internal/protocol`：编码 / 解码 / XOR round-trip；REGISTER 解析；错误 MAGIC / 超长 len。
- `internal/session`：心跳超时状态机；exec 串行队列。
- `internal/api`：login 成功 / 失败；未授权 401；创建监听器；对假会话 exec 409。
- 协议兼容：用 Go 写出一帧，C 侧（或纯 Go 复现 C 算法）解开，保证与现密钥一致。
- 前端：登录表单校验；无 token 跳登录。不做 E2E 浏览器测试。

## 12. 后续分期（不在本 spec 实现）

- 第二期：Web 点选生成 Windows exe（复用 Evasion-SubAgents loader 模板）、HTTP/WS 监听器。
- 第三期：文件上传下载、交互式终端、SOCKS。
- 免杀迭代：握手随机密钥、AES 替换 XOR、beacon 抖动；可选第二套 Rust implant，协议兼容即可。

## 13. 工具约定

- Git：本仓库独立版本控制。
- Codegraph：`codegraph init` 后用 `codegraph query/explore` 定位，不靠全文盲搜。
- Ant Design：Claude skill 在 `~/.claude/skills/ant-design`；CLI `@ant-design/cli`；MCP `antd mcp`。不把 ant-design 上游源码嵌进本仓库，运行时 `npm install antd`。
