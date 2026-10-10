#!/bin/sh
# 将 openos 快照的 node_modules 链接到 app/web/node_modules：
# openos 源码位于 app/web 目录树之外，TS/Vite 从文件位置向上解析不到依赖，
# 链接后快照源码与宿主共用同一份依赖（快照重克隆后需重跑，run.sh 已自动调用）。
# 注意保持 POSIX sh 兼容：CI 的 node:22-alpine 容器无 bash（postinstall 以 sh 调用）。
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LINK="$ROOT/.run/openos/web/node_modules"
# 必须用绝对路径：OPENOS=本地工作树形态下 .run/openos 本身是软链，链接实际
# 落在真实树上，相对路径 ../../../app/web 会按真实位置解析而错位
TARGET="$ROOT/app/web/node_modules"
mkdir -p "$ROOT/.run/openos/web"
if [ -L "$LINK" ] && [ "$(readlink "$LINK")" = "$TARGET" ]; then exit 0; fi
rm -rf "$LINK"
ln -s "$TARGET" "$LINK"
echo "== openos node_modules 链接就绪 → app/web/node_modules"
