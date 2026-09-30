#!/usr/bin/env bash
# OpenOS 生产运行脚本：后台守护 + PID 管理
#
# 用法：
#   ./run.sh start     构建前端与后端（= make build），然后后台启动并等待就绪
#   ./run.sh stop      优雅停止（SIGTERM → 最长等 10s → SIGKILL）
#   ./run.sh restart   重启 = stop + start（同样包含构建）
#
# 说明：
#   - 进程工作目录固定为 server/，与开发模式一致：数据落在 server/data/
#     （openos.db、homes/、goder 组件等）；前端静态目录以绝对路径注入 OPENOS_DIST，
#     避免相对 CWD 解析不到 web/dist
#   - 环境变量原样透传（OPENOS_ADDR / OPENOS_DATA / OPENOS_JWT_SECRET / OPENOS_AI_* 等）
#   - 服务日志追加写入 server/openos-server.log
#
# 注意：变量统一写作 ${VAR}——macOS 自带 bash 3.2 在非 UTF-8 locale 下会把
# 紧邻的全角标点并进 $VAR 形式的变量名（$ADDR） 会报 unbound variable）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="${ROOT}/server"
BIN="${SERVER_DIR}/openos-server"
PID_FILE="${SERVER_DIR}/openos-server.pid"
LOG_FILE="${SERVER_DIR}/openos-server.log"

ADDR="${OPENOS_ADDR:-127.0.0.1:8081}"
# 健康检查地址：监听 ":8081" 这类省略主机的写法时补上 127.0.0.1
case "${ADDR}" in
  :*) HEALTH_ADDR="127.0.0.1${ADDR}" ;;
  *)  HEALTH_ADDR="${ADDR}" ;;
esac

# 从 pidfile 读取存活且确为 openos-server 的 PID（校验进程身份，防止 PID 复用误杀）
pid_from_file() {
  [ -f "${PID_FILE}" ] || return 1
  local pid
  pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null || return 1
  ps -p "${pid}" -o args= 2>/dev/null | grep -q "openos-server" || return 1
  echo "${pid}"
}

# 查找当前实例 PID：优先 pidfile，兜底按「进程名 + 本仓库可执行路径」匹配
find_pid() {
  local pid p
  if pid="$(pid_from_file)"; then echo "${pid}"; return 0; fi
  for p in $(pgrep -x openos-server 2>/dev/null); do
    if ps -p "${p}" -o args= 2>/dev/null | grep -q "${SERVER_DIR}/openos-server"; then
      echo "${p}"
      return 0
    fi
  done
  return 1
}

build() {
  echo "→ 构建前端（web → web/dist）..."
  (cd "${ROOT}/web" && npm install --no-audit --no-fund && npm run build)
  echo "→ 构建后端（server → openos-server）..."
  (cd "${SERVER_DIR}" && go build -o openos-server .)
}

start() {
  local pid
  if pid="$(find_pid)"; then
    echo "OpenOS 已在运行（PID ${pid} → http://${ADDR}）；如需重新构建并重启，请执行 ./run.sh restart"
    return 0
  fi
  rm -f "${PID_FILE}"

  build

  echo "→ 启动 OpenOS（http://${ADDR}）..."
  # 工作目录 = server/：数据目录默认 data（即 server/data），与文档约定一致；
  # OPENOS_DIST 缺省时以绝对路径注入（尊重外部已设置的值）
  (
    cd "${SERVER_DIR}"
    OPENOS_DIST="${OPENOS_DIST:-${ROOT}/web/dist}" nohup "${BIN}" >> "${LOG_FILE}" 2>&1 &
    echo $! > "${PID_FILE}"
  )
  local spid
  spid="$(cat "${PID_FILE}")"

  # 等待端口就绪（最多 10s）；进程提前退出则立即报错并打印日志
  local i
  for i in $(seq 1 50); do
    if curl -s -o /dev/null --max-time 1 "http://${HEALTH_ADDR}/" 2>/dev/null; then
      echo "✓ OpenOS 已启动：PID ${spid} → http://${ADDR}（日志：${LOG_FILE}）"
      return 0
    fi
    if ! kill -0 "${spid}" 2>/dev/null; then
      echo "✗ 启动失败，最近日志如下：" >&2
      tail -n 20 "${LOG_FILE}" >&2 || true
      rm -f "${PID_FILE}"
      return 1
    fi
    sleep 0.2
  done
  echo "⚠ 已启动（PID ${spid}），但端口 ${HEALTH_ADDR#*:} 在 10s 内未就绪，请查看日志：${LOG_FILE}"
}

stop() {
  local pid
  if ! pid="$(find_pid)"; then
    rm -f "${PID_FILE}"
    echo "OpenOS 未在运行"
    return 0
  fi
  echo "→ 停止 OpenOS（PID ${pid}，优雅停机）..."
  kill "${pid}" 2>/dev/null || true
  local i
  for i in $(seq 1 100); do # 最长等 10s
    kill -0 "${pid}" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "${pid}" 2>/dev/null; then
    echo "  未在 10s 内退出，发送 SIGKILL"
    kill -9 "${pid}" 2>/dev/null || true
  fi
  rm -f "${PID_FILE}"
  echo "✓ 已停止"
}

restart() {
  stop
  start
}

usage() {
  cat <<'EOF'
用法: ./run.sh <command>

  start     构建前端与后端（= make build），后台启动并等待就绪
  stop      优雅停止（SIGTERM，10s 后强杀）
  restart   重启 = stop + start（同样包含构建）

环境变量原样透传给服务端（OPENOS_ADDR / OPENOS_DATA / OPENOS_JWT_SECRET / OPENOS_AI_* 等）。
EOF
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  restart) restart ;;
  -h|--help|help) usage ;;
  *) usage >&2; exit 1 ;;
esac
