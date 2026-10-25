# c2-platform

授权防御研究 / 红队训练用的最小 C2 工作台。

第一期：Go team server + React/antd 控制台 + Go implant（xor-tcp），C 版 implant 保留在 `implant/`。

## 启动

```bash
cp conf/setting.conf.example conf/setting.conf
# 修改 web_password
make web
make server
./bin/server -config conf/setting.conf
```

浏览器打开 `http://127.0.0.1:8082`。

Implant 交叉编译：

```bash
make implant      # 本机平台
make templates    # 五个目标平台模板
```

仅在授权隔离环境使用。不要对未授权目标部署。

## 浏览器信息

主机页「浏览器信息」标签：扫描 Chrome / Edge / Brave / Chromium / Opera / Firefox，列出配置文件、数据文件元信息（大小、最后修改）、书签、扩展、配置身份与加密方案摘要。

在任一数据文件上可以直接查询库内容（服务端用 `sqlite3` 解析，文件由 implant 传回）：

| 库 | 可查种类 |
| --- | --- |
| `History` | `history`、`downloads` |
| `Cookies` | `cookies`（仅 host / name / 过期时间） |
| `Web Data` | `autofill`、`formhistory` |
| `Login Data` | `logins`（URL / 用户名 / 使用次数，不含密码列） |
| `places.sqlite` | `history`、`bookmarks`、`formhistory`、`cookies` |

查询种类由 `internal/browser/query.go` 定义，`GET /api/sessions/{id}/browser/kinds` 可列出当前支持的集合。

每次扫描按主机名落盘到 `data/browser/<host>.json`（每台保留最近 20 次），页面上可切换历史扫描结果做对比。

行协议（implant → server，TSV）：

```
kind \t browser \t profile \t b \t c \t size \t path
F 文件产物   V 版本   B 书签   E 扩展   P 配置身份   K 加密方案摘要
```

## 凭据采集

两个入口，结果合并到同一份主机凭据库：

- `POST /api/sessions/{id}/browser/deep` — 浏览器深度采集：登录账号、Cookie、自动填表、配置身份。
- `POST /api/sessions/{id}/creds` `{"scope":"browser"|"system"|"all"}` — 系统侧凭据：WiFi、凭据管理器、FTP/终端客户端、PuTTY/RDP、Git、SSH 私钥、环境变量、包管理器 token。

行协议：

```
kind \t source \t target \t user \t secret \t note \t path
C 浏览器账号   S Cookie   A 自动填充   N 说明
W WiFi   M 凭据管理器   F FTP/客户端   P PuTTY   G Git
K SSH 私钥   E 环境变量   R RDP/远程   X 其他配置
```

### 解密在哪里做

主密钥永远不会离开目标主机，所以解密在 implant 内完成：

| 平台 | 浏览器密钥 | 算法 |
| --- | --- | --- |
| Windows | `Local State` 的 `os_crypt.encrypted_key`，DPAPI 解包 | AES-256-GCM（`v10`）；`v20` App-Bound 需提权注入，只记录不解 |
| macOS | 登录钥匙串 `<Browser> Safe Storage` | PBKDF2-HMAC-SHA1(1003) → AES-128-CBC |
| Linux | libsecret / `secret-tool`，回退硬编码 `peanuts` | PBKDF2-HMAC-SHA1(1) → AES-128-CBC |

Firefox 的 `logins.json` 走 NSS（`key4.db` + 可选主密码），本版本只记录账号库条目与站点，不做解密。

SQLite 由 `internal/sqlread` 直接读页，无需 CGO、`sqlite3` 或第三方驱动；覆盖表 b-tree、内页右指针、溢出页链与全部序列类型。已知限制：不合并 `-wal`，读到的是最后一次 checkpoint 的状态。

### 存储与可见性

- 落盘：`data/creds/<host>.json`，权限 `0600`，同一条记录按 `kind+source+target+user+secret` 去重，单主机上限 5000 条。
- 列表接口只返回脱敏预览（前 2 + 后 2），明文要显式 `POST /api/creds/{host}/reveal {"ids":[...]}` 才下发。
- `GET /api/creds/{host}/export` 导出 UTF-8 BOM 的 CSV（含明文，供离线核对）；`DELETE` 清除该主机全部记录。

## 自测

```bash
go test ./...                                  # 全量
go test ./internal/sqlread -v                  # SQLite 读取器（含 2500 行/溢出页/CLI 交叉校验）
go test ./internal/creds -race                 # 去重、脱敏、导出、并发写
go test ./internal/api -run TestCreds -v       # 模拟 implant 走通采集→脱敏→recover→导出→删除
```

`internal/api/live_test.go` 需要一个正在运行、口令为 `admin/changeme` 的守护进程，属于人工联调探针，CI 里通常跳过。
