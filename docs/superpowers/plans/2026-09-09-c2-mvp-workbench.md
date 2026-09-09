# C2 MVP 工作台 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地一个可登录的中文 Web 工作台：创建 TCP 监听器、Windows implant 上线、多会话分别 exec 并回显。

**Architecture:** 单个 Go team server embed React/antd 控制台；控制面 HTTP/WS `:8082`，C2 面独立 `xor-tcp` 监听器；implant 为现有 C 帧协议加上 REGISTER/HEARTBEAT/重连。

**Tech Stack:** Go 1.22+（本机 1.27）、React 18、Vite、TypeScript、antd 6、C + mingw-w64、JWT、gorilla/websocket 或 nhooyr/websocket。

**Spec:** `docs/superpowers/specs/2026-09-09-c2-mvp-workbench-design.md`

## Global Constraints

- 仓库根：`/Users/yym/c2-platform`；独立 git，不写入 Evasion-SubAgents。
- 控制台默认 `0.0.0.0:8080` 不对：必须是 `web_ip` + `web_port=8082`。
- C2 与控制台端口分离；v1 只有 `xor-tcp` 监听器。
- 帧头 `[A1 B7][type u16 LE][len u32 LE][XOR payload]`，密钥与 `Evasion-SubAgents/custom-stage/implant.c` 一致。
- 单用户 JWT；`conf/setting.conf` gitignore，只提交 example。
- 前端：React 18 + Vite + TS + antd 6；一个 `ConfigProvider` + `zh_CN`；写组件前 `antd info`，改完 `antd lint`。
- 不做：文件管理、隧道、插件、屏幕、交互终端、载荷生成、SQLite、多操作员。
- 不运行 `implant.exe`；Go 单测 + 交叉编译成功即工程验收。
- 不把 Vshell 二进制、license、插件打进仓库。
- 完成后在仓库根 `codegraph init`。

---

## File map

| Path | Responsibility |
|---|---|
| `cmd/server/main.go` | 读 conf、embed dist、起 HTTP、信号退出 |
| `internal/config/config.go` | 解析 ini 风格 setting.conf |
| `internal/protocol/protocol.go` | 帧编解码、XOR、REGISTER 结构 |
| `internal/protocol/protocol_test.go` | 帧 / XOR / REGISTER 单测 |
| `internal/session/hub.go` | 会话表、心跳、任务队列 |
| `internal/session/hub_test.go` | 超时与串行队列 |
| `internal/listener/tcp.go` | 接受连接、跑协议循环 |
| `internal/auth/auth.go` | 登录与 JWT |
| `internal/api/server.go` | REST + WS |
| `internal/api/server_test.go` | login / 401 / 409 |
| `implant/implant.c` | 迁自 custom-stage 并加注册心跳重连 |
| `implant/Makefile` | mingw 交叉编译 |
| `web/` | React 控制台 |
| `conf/setting.conf.example` | 示例配置 |
| `.gitignore` | bin、dist、node_modules、setting.conf、implant.exe |

XOR 密钥（与现 implant 一致，测试 fixture 必须用这组）：

```
2C 91 4F E6 03 AA 7D 18 B5 62 CF 34 E9 8A 51 0D
F6 23 78 AC 41 9E 07 DC 65 38 90 4B 12 F9 06 B3
```

---

### Task 1: 仓库骨架、git、配置解析

**Files:**
- Create: `go.mod`（module `c2-platform`）
- Create: `.gitignore`
- Create: `README.md`
- Create: `conf/setting.conf.example`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `cmd/server/main.go`（先只打印配置并退出 0 的占位亦可，但应能 `-config` 启动后 bind 失败前打印 web 地址）

**Interfaces:**
- Consumes: 无
- Produces: `config.Load(path string) (*Config, error)`；`Config` 含 `WebTitle, WebIP, WebPort, Username, Password, JWTSecret, LogLevel string/int`

- [ ] **Step 1: git init + gitignore + go.mod**

```
cd /Users/yym/c2-platform
git init
go mod init c2-platform
```

`.gitignore` 至少包含：`bin/`、`web/dist/`、`web/node_modules/`、`conf/setting.conf`、`implant/implant.exe`、`.codegraph/`、`*.exe`。

- [ ] **Step 2: 写 config 单测（缺文件、缺端口、空 jwt 允许）**

```go
func TestLoadExample(t *testing.T) {
    cfg, err := Load("../../conf/setting.conf.example")
    if err != nil { t.fatal(err) }
    if cfg.WebPort != 8082 { t.Fatalf("port %d", cfg.WebPort) }
    if cfg.Username != "admin" { t.Fatal("username") }
}
```

- [ ] **Step 3: 实现 ini 解析（不要引入重量 ini 库；手写 key=value，跳过空行和 `#`）**
- [ ] **Step 4: `go test ./internal/config/` 通过**
- [ ] **Step 5: commit** `chore: scaffold repo and config loader`

---

### Task 2: 协议编解码

**Files:**
- Create: `internal/protocol/protocol.go`
- Create: `internal/protocol/protocol_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `const Magic0, Magic1 byte = 0xA1, 0xB7`
  - `type Type uint16` 以及 CMD/RSP 常量（与 spec 表一致）
  - `func Crypt(dst []byte, typ Type)`
  - `func Encode(typ Type, payload []byte) []byte`
  - `type Frame struct { Type Type; Payload []byte }`
  - `func Decode(r io.Reader) (Frame, error)` 拒绝 plen>8192、错误 MAGIC
  - `type Register struct { Ver uint8; PID uint32; Hostname, Username, OS, Arch, IP string }`
  - `func ParseRegister([]byte) (Register, error)` / `func (Register) Marshal() []byte`

- [ ] **Step 1: 先写 XOR 与 Encode/Decode round-trip 测试，含错误 MAGIC**
- [ ] **Step 2: 实现 Crypt/Encode/Decode**
- [ ] **Step 3: REGISTER marshal/parse 测试（含空字符串、超长 len）**
- [ ] **Step 4: `go test ./internal/protocol/` 通过**
- [ ] **Step 5: commit** `feat: xor-tcp frame codec and register payload`

---

### Task 3: 会话中心

**Files:**
- Create: `internal/session/hub.go`
- Create: `internal/session/hub_test.go`

**Interfaces:**
- Consumes: `protocol.Register`
- Produces:
  - `type Status string` (`online`,`stale`,`dead`)
  - `type Session struct { ID, ListenerID, RemoteAddr string; Reg protocol.Register; LastSeen time.Time; Status Status }`
  - `type Task struct { ID, SessionID, Cmd string; Output []byte; Done bool; Err string }`
  - `func NewHub(now func() time.Time) *Hub`
  - `func (h *Hub) Attach(listenerID, remote string, reg protocol.Register, send func(protocol.Type, []byte) error) *Session`
  - `func (h *Hub) Detach(id string)`
  - `func (h *Hub) Tick()` 按 30s/90s 更新状态
  - `func (h *Hub) EnqueueExec(sessionID, cmd string) (taskID string, err error)`
  - `func (h *Hub) OnFrame(sessionID string, f protocol.Frame)`
  - `func (h *Hub) List() []Session`
  - `func (h *Hub) Subscribe() <-chan Event`

- [ ] **Step 1: 测 Attach 生成 `s-` 前缀 ID；Tick 把超时会话标 stale/dead**
- [ ] **Step 2: 测同一会话第二条 exec 在第一条 RSP_END 前排队**
- [ ] **Step 3: 实现 Hub（全内存，mutex）**
- [ ] **Step 4: `go test ./internal/session/` 通过**
- [ ] **Step 5: commit** `feat: in-memory session hub and serial tasks`

---

### Task 4: xor-tcp 监听器

**Files:**
- Create: `internal/listener/manager.go`
- Create: `internal/listener/tcp.go`
- Test: `internal/listener/tcp_test.go`（本机 loopback 连一个假 implant 用 Go 发 REGISTER）

**Interfaces:**
- Consumes: `protocol.*`, `session.Hub`
- Produces:
  - `type Spec struct { ID, Name, Bind string; Port int }`
  - `func NewManager(h *session.Hub) *Manager`
  - `func (m *Manager) Start(spec Spec) error`
  - `func (m *Manager) Stop(id string) error`
  - `func (m *Manager) List() []Spec`

连接处理：Accept → Decode REGISTER → Hub.Attach → 写 RSP_OK(id) → 循环 Decode，HEARTBEAT 刷新 LastSeen，CMD 由 Hub 经 send 回调写出。

- [ ] **Step 1: 测 Start 后 Go 客户端连上发 REGISTER，List sessions 长度为 1**
- [ ] **Step 2: 实现 Manager/tcp**
- [ ] **Step 3: `go test ./internal/listener/` 通过**
- [ ] **Step 4: commit** `feat: xor-tcp listener`

---

### Task 5: 认证 + HTTP/WS API

**Files:**
- Create: `internal/auth/jwt.go`
- Create: `internal/api/server.go`
- Create: `internal/api/server_test.go`
- Modify: `cmd/server/main.go` 真正 Serve

**Interfaces:**
- Consumes: config, session.Hub, listener.Manager
- Produces: `func New(cfg *config.Config, hub *session.Hub, lm *listener.Manager) http.Handler`
- 路由按 spec 第 5 节。WS 把 hub.Subscribe 事件 JSON 写出。
- JWT：`github.com/golang-jwt/jwt/v5`；secret 空则启动时 32 字节随机。

- [ ] **Step 1: httptest 测错误密码 401、正确密码返回 token、无 token 访问 /api/me 401**
- [ ] **Step 2: 测 POST listener 后 GET list 含该项；对不存在 session exec 409**
- [ ] **Step 3: 实现 handler；静态文件稍后 Task 7 embed，此时 404 即可**
- [ ] **Step 4: `go test ./internal/auth/ ./internal/api/` 通过**
- [ ] **Step 5: commit** `feat: login jwt and console REST/WS`

---

### Task 6: C implant

**Files:**
- Create: `implant/implant.c`（从 `/Users/yym/Evasion-SubAgents/custom-stage/implant.c` 复制后改）
- Create: `implant/Makefile`

行为增量：connect 退避循环；REGISTER；10s HEARTBEAT；QUIT 后重连而非 return。host/port 仍 XOR。EXEC 路径不改。

- [ ] **Step 1: 复制并加上 REGISTER/HEARTBEAT/reconnect**
- [ ] **Step 2: `make -C implant` 交叉编译成功（`file` 显示 PE32+）**
- [ ] **Step 3: 不要运行 exe**
- [ ] **Step 4: commit** `feat: windows implant with register and heartbeat`

---

### Task 7: React + antd 控制台

**Files:**
- Create: `web/` Vite React-TS 工程
- Pages: Login、Listeners、Sessions、SessionDetail、PayloadHelp
- Layout: antd `Layout` + `Menu`，菜单文案与 spec 第 9 节一致

**Interfaces:**
- Consumes: `/api/*`；开发期 vite proxy `'/api' -> http://127.0.0.1:8082`
- Produces: `npm run build` 输出 `web/dist`

约束：根 `ConfigProvider` + `zh_CN`；Table `rowKey`；写前 `antd info`，改完 `antd lint web/src --format json`。

- [ ] **Step 1: `npm create vite@latest web -- --template react-ts` 并安装 `antd @ant-design/icons`**
- [ ] **Step 2: 登录页 + token 存 localStorage + 无 token 跳 /login**
- [ ] **Step 3: 监听管理 Table + Drawer 创建**
- [ ] **Step 4: 主机列表 + 详情页 exec 输入和回显；WS 订阅**
- [ ] **Step 5: 上线说明只读页（Makefile 命令）**
- [ ] **Step 6: `npm run build` 成功；`antd lint` 无 error**
- [ ] **Step 7: commit** `feat: antd console for listeners and sessions`

---

### Task 8: embed 前端并打通 main

**Files:**
- Modify: `cmd/server/main.go` `//go:embed` 需要 `web/dist` 存在；若无 dist，开发模式只提供 API
- Create: `Makefile` 根：`make web`、`make server`、`make implant`

- [ ] **Step 1: embed dist 到 `/`；API 挂 `/api/`**
- [ ] **Step 2: `go build -o bin/server ./cmd/server` 成功**
- [ ] **Step 3: README 写启动：复制 example conf、`./bin/server`、浏览器 `:8082`**
- [ ] **Step 4: commit** `feat: embed console and single-binary server`

---

### Task 9: codegraph 索引

- [ ] **Step 1: `codegraph init /Users/yym/c2-platform`**
- [ ] **Step 2: `codegraph status` 确认 Go/TS/C 已索引**
- [ ] **Step 3: 不把 `.codegraph/` 提交**

---

## Execution notes

- 按 Task 1→9 顺序。每个 Task 的测试不过不要进入下一个。
- 不要启动或拷贝 `/tmp/vshell-ref/v_linux_amd64`。
- 不要把 Vshell zip 解压进本仓库。
- 实现中若与 spec 冲突，以 spec 为准。
