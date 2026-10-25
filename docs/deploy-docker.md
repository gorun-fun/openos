# Docker 部署

OpenOS 以多阶段 Docker 镜像交付（node → go → alpine），运行镜像非 root、数据全部落在 `/data` 卷。本文覆盖从零构建到升级备份的完整流程。

## 前置要求

- Docker 20.10+（含 BuildKit——Dockerfile 使用缓存挂载，老版本需 `DOCKER_BUILDKIT=1`）与 docker compose v2
- 构建机可访问：
  - `git.gorun.fun`（克隆 goder 源码）
  - `download.gorun.fun`（下载 gh 可执行文件）
  - `registry.npmmirror.com` / `goproxy.cn` / `mirrors.aliyun.com`（镜像内固定使用的国内源，境外网络同样可用，只是稍慢）

## 第一步：准备构建上下文（关键）

镜像构建**不在 Dockerfile 内克隆 goder / 下载 gh**（RUN 层缓存会锁死旧快照），而是由构建方先取进 `.run/` 目录，Dockerfile 以 `COPY` 消费。**跳过此步直接 build 会报 `COPY failed`**：

```bash
mkdir -p .run

# 1) goder 源码（AI IDE 组件）
git clone --depth 1 https://git.gorun.fun/share/goder.git .run/goder-src

# 2) gh 可执行文件（goder 运行期依赖）
VER=$(sh .run/goder-src/scripts/version.sh gh)   # 解析最新发布版
PLAT=linux                                        # arm64 机器改为 linux-arm64
curl -fsSL -o .run/gh "https://download.gorun.fun/grf-harness/${PLAT}/${VER}/gh"
chmod +x .run/gh && .run/gh --version
```

> 不要内置 AI 也能跑？可以——但那样 goder 应用只显示部署引导。`.run/` 是构建期依赖，运行期组件全部进镜像，部署机无需再装任何东西。

## 第二步：启动

```bash
# 本地/单机（数据持久化到宿主机 ./data；macOS 默认无法写 /app，务必设 OPENOS_DATA_DIR）
OPENOS_DATA_DIR=./data docker compose up -d --build

# 服务器（默认落宿主机 /app/docker/openos）
docker compose up -d --build

# → http://localhost:8091   （宿主 8091 → 容器 8081）
```

首次启动自动创建管理员 `admin/admin123`——**立即登录并在「系统设置 → 账户」修改密码**（或 `OPENOS_SEED_DEMO=0` 关闭演示账号，见下）。

## compose 环境变量

`docker-compose.yml` 预置了以下环境变量（按需修改后 `docker compose up -d` 生效）：

| 变量 | compose 默认 | 说明 |
|---|---|---|
| `OPENOS_DATA_DIR` | `/app/docker/openos` | 宿主机数据目录（bind mount → 容器 `/data`） |
| `OPENOS_AI_BASE_URL` / `_API_KEY` / `_MODEL` | 空 | AI 首启回填；更推荐启动后在「系统设置 → AI 服务」在线配置 |
| `OPENOS_JWT_SECRET` | 空（自动生成并持久化） | 多实例/重建容器保持会话时须显式固定 |
| `OPENOS_REAL_SHELL` | `on` | **演示配置**：开启真实终端。公网部署请改 `off`（见[安全说明](security.md)） |
| `OPENOS_EXCHANGE_SECRET` | 空 | 宿主集成密钥（不用宿主集成则留空 = 禁用） |
| `OPENOS_ROOM_URL` | 空 | 游戏房间服务地址（不用游戏可留空） |

镜像内已固定：`OPENOS_ADDR=0.0.0.0:8081`、`OPENOS_DATA=/data`、`OPENOS_DIST=/app/web/dist`、goder 组件路径——一般无需覆盖。

## 镜像内含什么

- OpenOS 服务端（单二进制）+ 前端静态资源
- **goder 全套组件**：goder-serve 网关、goder.js 嵌入库、gh 可执行文件——部署即可用 AI IDE
- 运行依赖：ca-certificates、tzdata、git、bash（gh 工具契约要求）
- 以 `openos` 用户（uid 1000）运行；入口脚本先以 root 修正 `/data` 卷属主再降权

## 升级

```bash
git pull
# 重新准备构建上下文（goder/gh 可能已更新）
rm -rf .run && mkdir .run
git clone --depth 1 https://git.gorun.fun/share/goder.git .run/goder-src
VER=$(sh .run/goder-src/scripts/version.sh gh) && \
  curl -fsSL -o .run/gh "https://download.gorun.fun/grf-harness/linux/${VER}/gh" && chmod +x .run/gh

OPENOS_DATA_DIR=./data docker compose up -d --build
```

数据全在卷内（SQLite + 用户 home + 附件 + goder 会话），容器重建不丢失。

## 备份

数据卷即宿主机目录（如 `./data` 或 `/app/docker/openos`）：

```bash
# 方式一：停机拷贝（最简单，数据一致）
docker compose stop && tar -czf openos-backup-$(date +%F).tar.gz ./data && docker compose start

# 方式二：在线备份（SQLite 用 .backup 保持一致，其余文件 rsync）
sqlite3 ./data/openos.db ".backup /tmp/openos-backup.db"
rsync -a ./data/ backup-host:/backup/openos/
```

## HTTPS 反向代理

WebSocket 事件总线需要 Upgrade 头，Nginx 示例：

```nginx
server {
    listen 443 ssl http2;
    server_name os.example.com;
    ssl_certificate     /etc/nginx/cert.pem;
    ssl_certificate_key /etc/nginx/cert.key;

    location / {
        proxy_pass http://127.0.0.1:8091;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;      # WebSocket 必需
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;                    # 长连接
        client_max_body_size 32m;                    # 文件上传（单文件上限 8MB，留余量）
    }
}
```

反代之后建议设置 `OPENOS_TRUST_PROXY=1`（防爆破计数按 `X-Forwarded-For` 末位分桶，单层反代语义）。

## 健康检查与排障

- `GET /api/health`：公开健康端点（含版本号），可直接给负载均衡 / 探活用
- `docker compose logs -f openos`：服务日志（启动时打印监听地址、AI 配置状态、goder 状态）
- 前端打不开（404/空白）：多为构建时 web 阶段产物异常，整体重建 `docker compose build --no-cache && docker compose up -d`
- goder 应用提示部署引导：镜像正常时不应出现；多为构建时 `.run/` 组件缺失或架构选错（arm64 机器取了 `linux` 版 gh）
