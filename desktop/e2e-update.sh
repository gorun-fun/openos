#!/bin/bash
# openos-desktop 自动更新本地 e2e：
#   旧版(0.0.50)客户端 + 假发布服务器(python http.server, 通道目录 macos-arm64/0.0.99)
#   → /api/version 检出更新 → /api/update 下载+sha256 校验+换文件 → exec 原位重启(同 PID)
#   → 新实例 /api/version 报告 0.0.99
set -u
T=/tmp/osd-e2e
SRV_PORT=18800
CONF_PORT=18801
cd "$(dirname "$0")"

rm -rf "$T" && mkdir -p "$T/bin" "$T/srv/macos-arm64/0.0.99" "$T/profile"

echo "== 构建旧版(0.0.50, 被测)与新版(0.0.99, 更新载荷) =="
CGO_ENABLED=0 go build -ldflags "-X main.version=0.0.50" -o "$T/bin/openos-desktop" . || exit 1
CGO_ENABLED=0 go build -ldflags "-X main.version=0.0.99" -o "$T/srv/macos-arm64/0.0.99/openos-desktop-macos-arm64" . || exit 1
shasum -a 256 "$T/srv/macos-arm64/0.0.99/openos-desktop-macos-arm64" \
  > "$T/srv/macos-arm64/0.0.99/openos-desktop-macos-arm64.sha256"

echo "== 起假发布服务器 :$SRV_PORT =="
(cd "$T/srv" && python3 -m http.server "$SRV_PORT" >/dev/null 2>&1) &
echo $! > "$T/srv.pid"
sleep 1
# 自检：目录索引格式与解析口径（href="<数字>/"）
curl -s "http://127.0.0.1:$SRV_PORT/macos-arm64/" | grep -o 'href="[0-9][^"/]*/"' || { echo "✗ 目录索引格式不符"; exit 1; }

echo "== 起被测客户端(0.0.50, 独立 profile) =="
(OPENOS_DESKTOP_UPDATE_URL="http://127.0.0.1:$SRV_PORT" \
  nohup "$T/bin/openos-desktop" -profile "$T/profile" -conf-port "$CONF_PORT" \
  > "$T/client.log" 2>&1 & echo $! > "$T/client.pid")
OLD_PID=$(cat "$T/client.pid")
echo "client pid=$OLD_PID"
sleep 4

echo "== 1) 版本检查（fresh 强制重查）=="
V=$(curl -s "http://127.0.0.1:$CONF_PORT/api/version?fresh=1")
echo "$V"
echo "$V" | grep -q '"current":"0.0.50"' || { echo "✗ 当前版本不符"; exit 1; }
echo "$V" | grep -q '"latest":"0.0.99"' || { echo "✗ 未检出最新版本"; exit 1; }
echo "$V" | grep -q '"hasUpdate":true' || { echo "✗ 未判定可更新"; exit 1; }

echo "== 2) 触发更新 =="
curl -s -X POST "http://127.0.0.1:$CONF_PORT/api/update"; echo
# 轮询到 restarting（下载+校验+换文件+1.2s 延迟）
RESTART=""
for i in $(seq 1 40); do
  S=$(curl -s "http://127.0.0.1:$CONF_PORT/api/version")
  if echo "$S" | grep -q '"restarting":true'; then RESTART=1; echo "restarting: $S"; break; fi
  sleep 0.5
done
[ -n "$RESTART" ] || { echo "✗ 未进入重启态"; tail -5 "$T/client.log"; exit 1; }

echo "== 3) 等 exec 重启完成（同 PID）=="
sleep 5
NEW_V=$(curl -s "http://127.0.0.1:$CONF_PORT/api/version?fresh=1")
echo "$NEW_V"
echo "$NEW_V" | grep -q '"current":"0.0.99"' || { echo "✗ 重启后版本仍为旧值"; tail -10 "$T/client.log"; exit 1; }
echo "$NEW_V" | grep -q '"hasUpdate":false' || { echo "✗ 更新后仍提示可更新"; exit 1; }
if ! kill -0 "$OLD_PID" 2>/dev/null; then
  echo "✗ 原 PID $OLD_PID 已不存在（exec 应保持同 PID）"
  exit 1
fi
echo "✓ 同 PID $OLD_PID 存活（exec 原位替换）"
grep -q "版本:     0.0.99" "$T/client.log" && echo "✓ 新实例横幅版本 0.0.99"
grep -q "已更新到 0.0.99" "$T/client.log" && echo "✓ 更新日志在案"

echo "== 4) 磁盘文件已替换 + .old 已被新实例清理 =="
"$T/bin/openos-desktop" -version | grep -q "0.0.99" && echo "✓ 二进制文件已是 0.0.99"
[ ! -f "$T/bin/openos-desktop.old" ] && echo "✓ .old 已清理" || echo "⚠ .old 仍在（不致命）"

echo "== 清理 =="
kill "$OLD_PID" 2>/dev/null
kill "$(cat "$T/srv.pid")" 2>/dev/null
sleep 1
echo "✓✓ e2e 全链路通过"
