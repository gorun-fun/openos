# OpenOS 生产镜像：前端构建 → Go 静态编译 → 最小运行镜像
FROM node:22-alpine AS web
# 国内 npm 镜像源（Runner 网络无法直连 npmjs）
ENV npm_config_registry=https://registry.npmmirror.com
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS server
# 国内 Go 模块代理（Runner 网络无法直连 proxy.golang.org，go mod 报错的根因）
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /build/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /openos .

FROM alpine:3.20
# 国内 Alpine 源（与 .gitlab-ci.yml 部署阶段同款）；su-exec 用于入口降权
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata su-exec && adduser -D -u 1000 openos
WORKDIR /app
COPY --from=server /openos /app/openos
COPY --from=web /build/web/dist /app/web/dist
COPY --chmod=755 docker-entrypoint.sh /app/docker-entrypoint.sh
ENV OPENOS_ADDR=0.0.0.0:8081 OPENOS_DATA=/data OPENOS_DIST=/app/web/dist
VOLUME /data
EXPOSE 8081
# 入口以 root 修正 /data 卷属主后降权为 openos 运行（见 docker-entrypoint.sh）
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/openos"]
