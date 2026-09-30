# OpenOS 生产镜像：前端构建 → Go 静态编译 → 最小运行镜像
# goder 组件（goder-serve 网关 + goder.js 嵌入库 + gh）随镜像分发，部署即可用

# ---- goder 源码（克隆自官方仓库，GODER_REPO 可覆盖/锁定）----
FROM alpine:3.20 AS goder-src
ARG GODER_REPO=https://git.gorun.fun/share/goder.git
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache git \
    && git clone --depth 1 ${GODER_REPO} /goder

# ---- goder 嵌入库 goder.js（与上游 ./build.sh embed 同款两步构建）----
FROM node:22-alpine AS goder-web
# 国内 npm 镜像源（Runner 网络无法直连 npmjs）
ENV npm_config_registry=https://registry.npmmirror.com
COPY --from=goder-src /goder /goder
WORKDIR /goder/frontend
RUN npm ci --no-audit --no-fund && npm run build && npm run build:embed

# ---- goder-serve 网关（纯 Go 无 CGO，静态编译）----
FROM golang:1.26-alpine AS goder-serve
# 国内 Go 模块代理（Runner 网络无法直连 proxy.golang.org，go mod 报错的根因）
ENV GOPROXY=https://goproxy.cn,direct
COPY --from=goder-src /goder /goder
WORKDIR /goder
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/goder-serve ./cmd/goder-serve

# ---- gh（grf-harness 发行版，按目标架构取最新版；目录结构 {平台}/{版本}/gh）----
FROM alpine:3.20 AS gh
ARG TARGETARCH
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache curl \
    && PLAT=$([ "$TARGETARCH" = "arm64" ] && echo linux-arm64 || echo linux) \
    && VER=$(curl -sL "https://download.gorun.fun/grf-harness/${PLAT}/" \
        | grep -o 'href="[0-9][^"]*/"' | sed 's/href="//;s|/"||' | sort -V | tail -1) \
    && echo "下载 gh ${VER} (${PLAT})" \
    && curl -sL -o /gh "https://download.gorun.fun/grf-harness/${PLAT}/${VER}/gh" \
    && chmod +x /gh && /gh --version

# ---- OpenOS 前端 ----
FROM node:22-alpine AS web
# 国内 npm 镜像源（Runner 网络无法直连 npmjs）
ENV npm_config_registry=https://registry.npmmirror.com
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- OpenOS 服务端 ----
FROM golang:1.26-alpine AS server
# 国内 Go 模块代理（Runner 网络无法直连 proxy.golang.org，go mod 报错的根因）
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /build/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /openos .

# ---- 运行镜像 ----
FROM alpine:3.20
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
