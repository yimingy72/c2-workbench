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
