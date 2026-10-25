# 自部署（裸机 / 二进制）

不使用 Docker，从源码构建或在服务器上直接运行 OpenOS。适用于自定义环境、内网部署或想精细控制的场景。

## 前置要求

| 依赖 | 版本 | 用途 |
|---|---|---|
| Go | ≥ 1.26 | 构建服务端 |
| Node.js + npm | ≥ 20（推荐 22） | 构建前端 |
| git | — | 拉取源码；goder 自动修复功能需要 |
| gh 可执行文件 | — | **可选**，仅 goder AI IDE 需要（PATH 上可直接发现，或 `OPENOS_GH_BIN` 指定路径） |

> SQLite 驱动为纯 Go 实现（modernc），**无 CGO 依赖**，任意平台 `CGO_ENABLED=0` 均可编译运行。

## 构建

```bash
git clone https://github.com/gorun-fun/openos.git && cd openos

# 生产构建：前端 dist + 服务端单二进制
make build        # → server/openos-server + web/dist

# （可选）构建 goder AI IDE 组件
make goder        # → server/data/bin/goder-serve + server/data/goder/goder.js
                  #   源码自动克隆自 git.gorun.fun/share/goder.git，需 go/npm/git
                  #   运行期另需 PATH 上有 gh（GODER_REPO/GODER_SRC 可覆盖源码位置）

# 跨平台发布包（darwin/linux × amd64/arm64）
make release      # → release/openos-<os>-<arch>.tar.gz
```

> `make release` 的 tar 包只含服务端二进制；前端产物在 `web/dist`，分发时请一并拷贝（见下文 OPENOS_DIST）。

## 运行

**前端不是内嵌的**：服务端从 `OPENOS_DIST` 目录托管静态资源（默认 `web/dist`，相对进程工作目录解析）。三种启动方式任选：

### 方式一：run.sh（推荐，本机/单机）

```bash
./run.sh start server    # 构建（若需）→ 启动 → 等待就绪；日志 server/openos-server.log
./run.sh stop server     # 停止
./run.sh restart server  # 重启（含构建）
```

run.sh 自动注入绝对路径的 `OPENOS_DIST`，数据落在 `server/data/`。

### 方式二：手动启动

```bash
cd server
OPENOS_DIST=$PWD/../web/dist ./openos-server
# → http://127.0.0.1:8081
```

### 方式三：systemd（生产）

```ini
# /etc/systemd/system/openos.service
[Unit]
Description=OpenOS server
After=network.target

[Service]
Type=simple
User=openos
WorkingDirectory=/opt/openos/server
Environment=OPENOS_ADDR=127.0.0.1:8081
Environment=OPENOS_DATA=/opt/openos/server/data
Environment=OPENOS_DIST=/opt/openos/web/dist
# Environment=OPENOS_JWT_SECRET=change-me-multi-instance-required
# Environment=OPENOS_GODER_BIN=/opt/openos/server/data/bin/goder-serve
# Environment=OPENOS_GODER_JS=/opt/openos/server/data/goder/goder.js
ExecStart=/opt/openos/server/openos-server
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now openos
```

部署目录建议（把仓库构建产物收拢到 `/opt/openos`）：`server/openos-server` 二进制、`web/dist` 前端、`server/data` 数据目录（首次自动创建）。

## 数据目录

`OPENOS_DATA`（默认 `data`，相对工作目录；run.sh 形态即 `server/data/`）：

```
data/
├── openos.db          # SQLite（用户/应用/角色/审计/设置；WAL 模式）
├── homes/<用户>/      # 每用户磁盘 home（文件管理/终端/goder 共用）
├── bin/goder-serve    # goder 网关（make goder 产物）
├── goder/goder.js     # goder 嵌入库
├── attachments/       # 系统附件
├── steward/           # 代码管家状态（启用时）
└── market/bundles/    # 商店组件应用
```

**删除该目录 = 数据全部丢失**；备份方式同 [Docker 部署](deploy-docker.md#备份)。

## 升级

```bash
git pull
make build && make goder     # 组件有更新时
./run.sh restart server      # 或 systemctl restart openos
```

数据目录不受构建影响，跨版本保留（SQLite 结构启动时自动迁移）。

## HTTPS

裸机部署同样建议置于 Nginx/Caddy 反代之后（配置同 [Docker 文档的 Nginx 示例](deploy-docker.md#https-反向代理)，`proxy_pass` 指向 `127.0.0.1:8081`）；或用 Caddy 一行：

```
os.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

## 常用启动配置

```bash
OPENOS_ADDR=0.0.0.0:8081 \        # 对外监听
OPENOS_JWT_SECRET=<随机长串> \     # 固定密钥（多实例/定期重建进程时保持登录态）
OPENOS_SEED_DEMO=0 \              # 不种演示账号
OPENOS_REAL_SHELL=on \            # 开启真实终端（公网部署保持默认 off）
OPENOS_AI_BASE_URL=... OPENOS_AI_API_KEY=... OPENOS_AI_MODEL=... \
./openos-server
```

完整变量见[配置参考](configuration.md)。
