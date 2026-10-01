#!/usr/bin/env bash
# OpenOS 运行脚本：纯进程管理（start/stop/restart，不构建）
#
# 用法：
#   ./run.sh start     构建前端与后端（= make build），然后启动并等待就绪。
#                       端口被其他进程占用（如 go run 开发实例）时直接杀掉占用进程再启动
#   ./run.sh stop      停止应用：本实例优雅停机（SIGTERM → 最长等 10s → SIGKILL），
#                       之后端口仍被任何进程占用则直接杀掉，确保端口释放
#   ./run.sh restart   重启 = 先 stop 再 start（同样包含构建）
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
PORT="${HEALTH_ADDR##*:}"

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

# 查找本实例 PID：优先 pidfile，兜底按「进程名 + 本仓库可执行路径」匹配。
# 识别不了相对路径（./openos-server）启动的手动实例——那类实例由 kill_port 按端口兜底处置
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

# 列出占用端口（LISTEN）的 PID；lsof 缺失时（部分精简 Linux）返回失败，调用方静默跳过。
# 必须带 -sTCP:LISTEN：不带会列出仅与端口有连接的客户端（如浏览器），导致误杀
port_pids() {
  command -v lsof >/dev/null 2>&1 || return 1
  lsof -nP -tiTCP:"${PORT}" -sTCP:LISTEN 2>/dev/null
}

# 端口被占用则直接杀掉占用进程（SIGKILL），并等端口释放（最多约 5s）
kill_port() {
  local pids pid i
  pids="$(port_pids)" || return 0
  if [ -z "${pids}" ]; then return 0; fi
  echo "→ 端口 ${PORT} 被占用，直接杀掉占用进程：" >&2
  ps -o pid=,command= -p "$(echo "${pids}" | tr '\n' ',' | sed 's/,$//')" >&2 || true
  for pid in ${pids}; do
    if [ "${pid}" -gt 1 ] 2>/dev/null && [ "${pid}" != "$$" ]; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done
  for i in $(seq 1 25); do
    pids="$(port_pids)" || return 0
    if [ -z "${pids}" ]; then return 0; fi
    sleep 0.2
  done
  echo "✗ 端口 ${PORT} 杀掉占用进程后仍未释放（剩余：${pids}）" >&2
  return 1
}

build() {
  echo "→ 构建前端（web → web/dist）..."
  (cd "${ROOT}/web" && npm install --no-audit --no-fund && npm run build)
  echo "→ 构建后端（server → openos-server）..."
  (cd "${SERVER_DIR}" && go build -o openos-server .)
}

start() {
  local pid spid i
  if pid="$(find_pid)"; then
    echo "OpenOS 已在运行（PID ${pid} → http://${ADDR}）；如需重新构建并重启，请执行 ./run.sh restart"
    return 0
  fi
  rm -f "${PID_FILE}"

  build

  # 端口被其他进程占用时（典型：go run 开发实例抢了 8081）直接杀掉再启动，
  # 避免「bind 失败进程退出、curl 却探测到占用者的响应而误报启动成功」。
  # 放在构建之后：构建期间旧实例继续提供服务，构建失败也不会动正在运行的进程
  if ! kill_port; then
    return 1
  fi

  echo "→ 启动 OpenOS（http://${ADDR}）..."
  # 工作目录 = server/：数据目录默认 data（即 server/data），与文档约定一致；
  # OPENOS_DIST 缺省时以绝对路径注入（尊重外部已设置的值）
  (
    cd "${SERVER_DIR}"
    OPENOS_DIST="${OPENOS_DIST:-${ROOT}/web/dist}" nohup "${BIN}" >> "${LOG_FILE}" 2>&1 &
    echo $! > "${PID_FILE}"
  )
  spid="$(cat "${PID_FILE}")"

  # 等待端口就绪（最多 10s）：每轮先确认进程仍存活（bind 失败等提前退出的情况），
  # 再以 curl -sf 探测（-f 视 4xx/5xx 为失败，防止探测请求打到端口上的其他服务
  # 拿到 404 响应也误判就绪）
  for i in $(seq 1 50); do
    if ! kill -0 "${spid}" 2>/dev/null; then
      echo "✗ 启动失败，最近日志如下：" >&2
      tail -n 20 "${LOG_FILE}" >&2 || true
      rm -f "${PID_FILE}"
      return 1
    fi
    if curl -sf -o /dev/null --max-time 1 "http://${HEALTH_ADDR}/" 2>/dev/null; then
      echo "✓ OpenOS 已启动：PID ${spid} → http://${ADDR}（日志：${LOG_FILE}）"
      return 0
    fi
    sleep 0.2
  done
  echo "⚠ 已启动（PID ${spid}），但端口 ${PORT} 在 10s 内未就绪，请查看日志：${LOG_FILE}"
}

stop() {
  local pid i
  if pid="$(find_pid)"; then
    echo "→ 停止 OpenOS（PID ${pid}，优雅停机）..."
    kill "${pid}" 2>/dev/null || true
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
  else
    rm -f "${PID_FILE}"
    echo "OpenOS 未在运行"
  fi
  # 兜底：端口仍被占用（pidfile 丢失的手动实例、go run 开发实例等）直接杀掉
  kill_port || true
}

restart() {
  stop
  start
}

usage() {
  cat <<'EOF'
用法: ./run.sh <command>

  start     构建前端与后端（= make build），然后启动并等待就绪。
            端口被其他进程占用时直接杀掉占用进程再启动
  stop      停止应用：优雅停机；之后端口仍被占用则直接杀掉占用进程
  restart   重启 = 先 stop 再 start（同样包含构建）

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
