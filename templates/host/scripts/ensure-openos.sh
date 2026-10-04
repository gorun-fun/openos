#!/usr/bin/env bash
# 确保 openos 源码快照（.run/openos）为远程仓库最新。
# 宿主应用消费 openos 桌面框架（前端 @openos 别名 + 服务端子进程二进制 + hostsdk），
# 统一从这份快照构建；缓存目录不做本地修改。
#
# 修改上游的正确姿势（工作约定）：
#   本地 ~/AiProjects/openos 修改 → 构建验证 → 推送 main → 回宿主工程 ./run.sh restart
#   （本脚本从公开链接拉取最新快照生效；紧急调试可设 OPENOS 指向本地工作树绕过）
#
# 用法: ./scripts/ensure-openos.sh
# 环境变量: OPENOS_SRC  远程地址（默认 https://git.gorun.fun/share/openos.git）
#           OPENOS_REF  分支或 tag（默认 main，始终跟踪最新）
#           OPENOS      显式指向本地 openos 仓库（调试上游，跳过拉取）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/.run/openos"
URL="${OPENOS_SRC:-https://git.gorun.fun/share/openos.git}"
REF="${OPENOS_REF:-main}"

mkdir -p "$ROOT/.run"

if [ -n "${OPENOS:-}" ]; then
  echo "== 使用本地 openos 工作树: $OPENOS"
  rm -rf "$DEST"
  ln -s "$OPENOS" "$DEST"
  exit 0
fi

if [ ! -d "$DEST/.git" ]; then
  echo "== 克隆 openos: $URL"
  rm -rf "$DEST"
  git clone --depth 1 "$URL" "$DEST"
  git -C "$DEST" fetch --depth 1 origin "$REF"
  git -C "$DEST" reset --hard FETCH_HEAD >/dev/null
else
  before="$(git -C "$DEST" rev-parse HEAD 2>/dev/null || echo none)"
  if git -C "$DEST" fetch --depth 1 origin "$REF"; then
    after="$(git -C "$DEST" rev-parse FETCH_HEAD)"
    if [ "$before" != "$after" ]; then
      echo "== openos 有更新: ${before:0:12} → ${after:0:12}"
      git -C "$DEST" reset --hard "$after" >/dev/null
    else
      echo "== openos 快照已是最新: ${after:0:12} ($URL@$REF)"
    fi
  else
    echo "警告: 拉取 $URL 失败（离线?），沿用本地快照 $(git -C "$DEST" rev-parse --short HEAD)" >&2
  fi
fi
