# OpenOS 生产镜像：前端构建 → Go 静态编译 → 最小运行镜像
FROM node:22-alpine AS web
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS server
WORKDIR /build/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /openos .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 openos
WORKDIR /app
COPY --from=server /openos /app/openos
COPY --from=web /build/web/dist /app/web/dist
USER openos
ENV OPENOS_ADDR=0.0.0.0:8420 OPENOS_DATA=/data OPENOS_DIST=/app/web/dist
VOLUME /data
EXPOSE 8420
ENTRYPOINT ["/app/openos"]
