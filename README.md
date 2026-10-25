# c2-platform

授权防御研究 / 红队训练用的最小 C2 工作台。

第一期：Go team server + React/antd 控制台 + Windows C implant（xor-tcp）。

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
make implant
```

仅在授权隔离环境使用。不要对未授权目标部署。

## 浏览器信息

主机页「浏览器信息」标签：一键扫描 Chrome / Edge / Brave / Chromium / Opera / Firefox，列出配置文件、数据文件元信息（大小、最后修改）、书签与扩展清单。

范围界定：只读取文件元数据与书签/扩展清单。`Login Data`、`Cookies`、`logins.json`、`key4.db` 等凭据存储不会被打开或解密，本模块不提供密码 / cookie 导出。

每次扫描按主机名落盘到 `data/browser/<host>.json`（每台保留最近 20 次），页面上可切换历史扫描结果做对比。
