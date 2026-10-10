# OpenOS 生产镜像：前端构建 → Go 静态编译 → 最小运行镜像
# goder 组件（goder-serve 网关 + goder.js 嵌入库 + gh）随镜像分发，部署即可用
# 基础镜像边界（2026-10-11 定）：FROM 一律引用公网 docker.io；私有仓库
# 10.8.0.6:5000 只做构建产物 push 与部署拉取；依赖（npm/go module/apk）走国内源。
# docker.io 不可直连时靠 Runner 本地预置镜像兜底（升级基础镜像版本须先在 .2 预拉）

# ---- goder 源码（构建方随上下文提供 .run/goder-src，CI 构建前 clone 最新版；
# 不能在镜像层内 clone——RUN 层缓存键只认命令字符串，上游 goder 前进而缓存
# 不失效，镜像会锁死旧快照，见 grf-hotel 同款教训）----
FROM alpine:3.24 AS goder-src
COPY .run/goder-src /goder

# ---- goder 嵌入库 goder.js（与上游 ./build.sh embed 同款两步构建）----
FROM node:22-alpine AS goder-web
# 国内 npm 镜像源（Runner 网络无法直连 npmjs）
ENV npm_config_registry=https://registry.npmmirror.com
COPY --from=goder-src /goder /goder
WORKDIR /goder/frontend
# BuildKit 缓存挂载：npm 下载缓存。goder 源码每次随上游变化（COPY 内容寻址），
# 层缓存必然穿透，缓存挂载是此处唯一的复用手段；id=goder-* 与 grf-play/grf-hotel
# 的 goder 阶段共享——同一份源码，依赖下载与编译结果跨项目复用。
# 硬约束：npm ci 必须单独成层——npm ci 会先整删 node_modules，若同层把 vite
# 缓存挂进 node_modules/.vite，挂载点 rmdir 必报 EBUSY（挂载即自动创建挂载点
# 目录，首次构建也失败；v0.0.18 CI 构建失败的根因）
RUN --mount=type=cache,id=goder-npm,target=/root/.npm npm ci --no-audit --no-fund
# vite 依赖预构建缓存与下方 web 阶段同款：本层只跑构建、不增删 node_modules，
# 挂进 node_modules/.vite 才安全
RUN --mount=type=cache,id=goder-vite,target=/goder/frontend/node_modules/.vite \
    npm run build && npm run build:embed

# ---- goder-serve 网关（纯 Go 无 CGO，静态编译）----
FROM golang:1.26-alpine AS goder-serve
# 国内 Go 模块代理（Runner 网络无法直连 proxy.golang.org，go mod 报错的根因）
ENV GOPROXY=https://goproxy.cn,direct
COPY --from=goder-src /goder /goder
WORKDIR /goder
# 缓存挂载：模块缓存 + 编译缓存（id 与 grf-play/grf-hotel 的 goder 阶段共享），
# 上游小改动时增量编译，依赖包与标准库不再重编
RUN --mount=type=cache,id=goder-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=goder-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/goder-serve ./cmd/goder-serve

# ---- gh（grf-harness 发行版，构建方随上下文提供 .run/gh，CI 构建前取最新版；
# 同样不能在镜像层内下载，层缓存会锁死旧版 gh）----
FROM alpine:3.24 AS gh
COPY --chmod=755 .run/gh /gh

# ---- OpenOS 前端 ----
FROM node:22-alpine AS web
# 国内 npm 镜像源（Runner 网络无法直连 npmjs）
ENV npm_config_registry=https://registry.npmmirror.com
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
# BuildKit 缓存挂载：npm 下载缓存跨构建复用；依赖未变时层缓存命中本层跳过，
# 依赖变更重装也只走缓存不走网络。id= 按项目隔离，避免与其他项目相互污染
RUN --mount=type=cache,id=openos-npm,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
# 挂载 vite 依赖预构建缓存（node_modules/.vite），依赖未变时跳过重新预打包
RUN --mount=type=cache,id=openos-vite,target=/build/web/node_modules/.vite npm run build

# ---- OpenOS 服务端 ----
FROM golang:1.26-alpine AS server
# 版本号（CI 传入 git tag，如 v0.0.7；缺省 dev），系统信息/健康检查展示用
ARG APP_VERSION=dev
# 国内 Go 模块代理（Runner 网络无法直连 proxy.golang.org，go mod 报错的根因）
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /build/server
COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,id=openos-gomod,target=/go/pkg/mod go mod download
COPY server/ ./
# 缓存挂载：模块缓存 + 编译缓存，源码变更时增量编译
RUN --mount=type=cache,id=openos-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=openos-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X openos/server/internal/store.Version=${APP_VERSION}" -o /openos .

# ---- 运行镜像 ----
FROM alpine:3.24
# 国内 Alpine 源（与 .gitlab-ci.yml 部署阶段同款）；su-exec 用于入口降权；git 供 gh 使用
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata su-exec git && adduser -D -u 1000 openos
WORKDIR /app
COPY --from=server /openos /app/openos
COPY --from=web /build/web/dist /app/web/dist
COPY --from=goder-serve /out/goder-serve /app/goder/goder-serve
COPY --from=goder-web /goder/frontend/dist-embed/goder.js /app/goder/goder.js
COPY --from=gh /gh /app/goder/gh
COPY --chmod=755 docker-entrypoint.sh /app/docker-entrypoint.sh
# goder 组件内置于镜像（每用户数据仍落 /data 卷）；外部可用同名环境变量覆盖
ENV OPENOS_ADDR=0.0.0.0:8081 OPENOS_DATA=/data OPENOS_DIST=/app/web/dist \
    OPENOS_GODER_BIN=/app/goder/goder-serve OPENOS_GODER_JS=/app/goder/goder.js OPENOS_GH_BIN=/app/goder/gh
VOLUME /data
EXPOSE 8081
# 入口以 root 修正 /data 卷属主后降权为 openos 运行（见 docker-entrypoint.sh）
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/openos"]
