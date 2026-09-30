#!/bin/sh
# OpenOS 容器入口：以 root 启动 → 修正数据卷属主 → 降权为 openos 用户运行服务。
# 命名卷/bind mount 首次初始化时属主为 root，非 root 进程无法在 /data 创建 SQLite 文件
# （报 "unable to open database file (14)"），故入口先 chown 再降权（nginx 官方镜像同款模式）。
# HOME 显式指到 openos 用户目录：gh 与 goder-serve 的种子逻辑需读写 ~/.grf，
# 不设置会继承 root 的 /root（不可写）。
set -e

if [ "$(id -u)" = "0" ]; then
  mkdir -p /home/openos
  chown -R openos:openos /data /home/openos
  export HOME=/home/openos
  exec su-exec openos:openos "$@"
fi
# 非 root 直接运行（docker run --user 1000 等场景）
exec "$@"
