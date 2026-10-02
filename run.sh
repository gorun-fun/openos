#!/usr/bin/env bash
# OpenOS 运行脚本：纯进程管理（start/stop/restart [server|desktop]）
#
# 用法：
#   ./run.sh start [desktop]   一键启动（默认 target = desktop）：先确保服务端运行
#                              （未运行则构建前端与后端 = make build 并等待就绪），
#                              再启动 openos-desktop 桌面客户端（CDP 驱动 Chrome
#                              app 模式打开 OpenOS，登录态持久于 ~/.openos-desktop）
#   ./run.sh start server      仅启动服务端（构建 + 启动 + 等待就绪）
#   ./run.sh stop [desktop]    停止整套：先桌面客户端、后服务端（默认）；
#                              stop server 仅停服务端；stop desktop 仅停客户端
#   ./run.sh restart [desktop] 重启（默认整套；restart server 仅服务端，同样含构建）
#
# 说明：
#   - 服务端工作目录固定为 server/，与开发模式一致：数据落在 server/data/
#     （openos.db、homes/、goder 组件等）；前端静态目录以绝对路径注入 OPENOS_DIST，
#     避免相对 CWD 解析不到 web/dist
#   - 桌面客户端二进制在仓库根 openos-desktop（缺失或可构建时自动构建；更新代码后
#     亦可手动 make desktop），日志追加写入 openos-desktop.log；打开地址默认
#     http://<OPENOS_ADDR>，OPENOS_DESKTOP_URL 可覆盖（开发模式可指 vite 的 5173）
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

DESKTOP_BIN="${ROOT}/openos-desktop"
DESKTOP_PID_FILE="${ROOT}/openos-desktop.pid"
DESKTOP_LOG="${ROOT}/openos-desktop.log"

ADDR="${OPENOS_ADDR:-127.0.0.1:8081}"
# 健康检查地址：监听 ":8081" 这类省略主机的写法时补上 127.0.0.1
case "${ADDR}" in
  :*) HEALTH_ADDR="127.0.0.1${ADDR}" ;;
  *)  HEALTH_ADDR="${ADDR}" ;;
esac
PORT="${HEALTH_ADDR##*:}"
# 桌面客户端打开的 OpenOS 地址：默认服务端健康地址；OPENOS_DESKTOP_URL 可覆盖
DESKTOP_URL="${OPENOS_DESKTOP_URL:-http://${HEALTH_ADDR}}"

# 判定 PID 是否本仓库的 openos-server 主进程：args 含本仓库可执行路径，且不是
# mcp-shim 子命令——openos-server mcp-shim 是 goder 桌面控制插件的 stdio↔HTTP 桥
# 子进程，由服务端拉起、args 同样含二进制路径，不排除会被误认成服务端（曾因此
# 误报「已在运行」跳过启动）
is_server_main() {
  local args
  args="$(ps -p "$1" -o args= 2>/dev/null)" || return 1
  case "${args}" in
    *mcp-shim*) return 1 ;;
  esac
  echo "${args}" | grep -q "${SERVER_DIR}/openos-server"
}

# 从 pidfile 读取存活且确为 openos-server 主进程的 PID（校验进程身份，防止 PID 复用误杀）
pid_from_file() {
  [ -f "${PID_FILE}" ] || return 1
  local pid
  pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null || return 1
  is_server_main "${pid}" || return 1
  echo "${pid}"
}

# 查找本实例 PID：优先 pidfile，兜底按「进程名 + 本仓库可执行路径」匹配。
# 识别不了相对路径（./openos-server）启动的手动实例——那类实例由 kill_port 按端口兜底处置
find_pid() {
  local pid p
  if pid="$(pid_from_file)"; then echo "${pid}"; return 0; fi
  for p in $(pgrep -x openos-server 2>/dev/null); do
    if is_server_main "${p}"; then
      echo "${p}"
      return 0
    fi
  done
  return 1
}

# 查找桌面客户端 PID：pidfile 优先（校验存活与身份）；兜底 pgrep 精确进程名——
# 专用 profile（~/.openos-desktop）决定本机同时只能有一个 openos-desktop 实例，
# 包括手工以相对路径启动的实例也一并纳管
find_desktop_pid() {
  local pid p
  if [ -f "${DESKTOP_PID_FILE}" ]; then
    pid="$(cat "${DESKTOP_PID_FILE}" 2>/dev/null || true)"
    if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null \
       && ps -p "${pid}" -o args= 2>/dev/null | grep -q "openos-desktop"; then
      echo "${pid}"
      return 0
    fi
  fi
  for p in $(pgrep -x openos-desktop 2>/dev/null); do
    echo "${p}"
    return 0
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

# 确保桌面客户端二进制存在：有 Go 就构建（增量，秒级）；构建失败但二进制可用则沿用
ensure_desktop_bin() {
  if command -v go >/dev/null 2>&1; then
    echo "→ 构建桌面客户端（desktop → openos-desktop）..."
    if (cd "${ROOT}/desktop" && go build -o "${DESKTOP_BIN}" .); then
      return 0
    fi
    if [ -x "${DESKTOP_BIN}" ]; then
      echo "⚠ 构建失败，沿用现有二进制 ${DESKTOP_BIN}" >&2
      return 0
    fi
    return 1
  fi
  if [ -x "${DESKTOP_BIN}" ]; then
    echo "⚠ 无 Go 工具链，沿用现有二进制 ${DESKTOP_BIN}"
    return 0
  fi
  echo "✗ 缺少 ${DESKTOP_BIN} 且无 Go 工具链，无法构建（可先执行 make desktop）" >&2
  return 1
}

start_server() {
  local pid spid i
  if pid="$(find_pid)"; then
    echo "OpenOS 服务端已在运行（PID ${pid} → http://${ADDR}）；如需重新构建并重启，请执行 ./run.sh restart"
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

  echo "→ 启动 OpenOS 服务端（http://${ADDR}）..."
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
      echo "✓ OpenOS 服务端已启动：PID ${spid} → http://${ADDR}（日志：${LOG_FILE}）"
      return 0
    fi
    sleep 0.2
  done
  echo "⚠ 已启动（PID ${spid}），但端口 ${PORT} 在 10s 内未就绪，请查看日志：${LOG_FILE}"
}

stop_server() {
  local pid i
  if pid="$(find_pid)"; then
    echo "→ 停止 OpenOS 服务端（PID ${pid}，优雅停机）..."
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
    echo "OpenOS 服务端未在运行"
  fi
  # 兜底：端口仍被占用（pidfile 丢失的手动实例、go run 开发实例等）直接杀掉
  kill_port || true
  # 清理本仓库 openos-server 拉起的 mcp-shim 孤儿（服务端非优雅退出时会残留）
  pkill -f -- "${SERVER_DIR}/openos-server mcp-shim" 2>/dev/null || true
}

start_desktop() {
  local pid spid i mark
  if pid="$(find_desktop_pid)"; then
    echo "桌面客户端已在运行（PID ${pid} → ${DESKTOP_URL}）；如需重启，请执行 ./run.sh restart desktop"
    return 0
  fi
  rm -f "${DESKTOP_PID_FILE}"

  ensure_desktop_bin || return 1

  # 就绪判定用：只看本次启动新增的日志（横幅「OpenOS 桌面客户端已启动」在 Chrome
  # 进程拉起后打印；Chrome 缺失/启动失败时客户端会在数秒内退出并留下原因）
  mark=0
  if [ -f "${DESKTOP_LOG}" ]; then mark=$(( $(wc -l < "${DESKTOP_LOG}") + 0 )); fi

  echo "→ 启动桌面客户端（${DESKTOP_URL}）..."
  (
    cd "${ROOT}"
    nohup "${DESKTOP_BIN}" -url "${DESKTOP_URL}" >> "${DESKTOP_LOG}" 2>&1 &
    echo $! > "${DESKTOP_PID_FILE}"
  )
  spid="$(cat "${DESKTOP_PID_FILE}")"

  for i in $(seq 1 40); do # 最多 10s
    if ! kill -0 "${spid}" 2>/dev/null; then
      echo "✗ 桌面客户端启动失败，最近日志如下：" >&2
      tail -n 20 "${DESKTOP_LOG}" >&2 || true
      rm -f "${DESKTOP_PID_FILE}"
      return 1
    fi
    if tail -n +"$((mark + 1))" "${DESKTOP_LOG}" 2>/dev/null | grep -q "桌面客户端已启动"; then
      echo "✓ 桌面客户端已启动：PID ${spid} → ${DESKTOP_URL}（日志：${DESKTOP_LOG}）"
      return 0
    fi
    sleep 0.25
  done
  echo "⚠ 桌面客户端进程在运行（PID ${spid}），但 10s 内未确认就绪，请查看日志：${DESKTOP_LOG}"
}

stop_desktop() {
  local pid i
  if pid="$(find_desktop_pid)"; then
    echo "→ 停止桌面客户端（PID ${pid}）..."
    kill "${pid}" 2>/dev/null || true
    for i in $(seq 1 100); do # 最长等 10s
      kill -0 "${pid}" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "${pid}" 2>/dev/null; then
      echo "  未在 10s 内退出，发送 SIGKILL"
      kill -9 "${pid}" 2>/dev/null || true
    fi
    rm -f "${DESKTOP_PID_FILE}"
    echo "✓ 已停止"
  else
    rm -f "${DESKTOP_PID_FILE}"
    echo "桌面客户端未在运行"
  fi
  # SIGKILL 兜底后 Chrome 可能残留（正常路径客户端会按进程组自清）：按专用 profile
  # 参数清理，只匹配默认 profile 且该参数后必跟其他参数（命令行中以空格分隔）
  pkill -f -- "--user-data-dir=${HOME}/.openos-desktop " 2>/dev/null || true
}

usage() {
  cat <<'EOF'
用法: ./run.sh <command> [target]

  start     启动（默认 target = desktop）
  stop      停止（默认 target = desktop）
  restart   重启（默认 target = desktop，同样包含构建）

target:
  desktop   整套 = 桌面客户端 + 服务端：
              start   先确保服务端运行（未运行则构建并启动），再启动 openos-desktop
                      （CDP 驱动 Chrome app 模式打开 OpenOS，登录态持久于 ~/.openos-desktop）
              stop    先停桌面客户端、再停服务端
  server    仅服务端（openos-server）。desktop 与 server 是两档目标：desktop=整套
            （客户端+服务端），server=仅服务端

示例：
  ./run.sh start              # 一键：服务端 + 桌面客户端
  ./run.sh start desktop      # 同上
  ./run.sh start server       # 仅服务端（= 旧版 ./run.sh start）
  ./run.sh stop               # 整套停止
  ./run.sh stop desktop       # 仅停桌面客户端（服务端继续运行）
  ./run.sh restart            # 整套重建并重启

环境变量原样透传给服务端（OPENOS_ADDR / OPENOS_DATA / OPENOS_JWT_SECRET / OPENOS_AI_* 等）；
OPENOS_DESKTOP_URL 可改桌面客户端打开地址（默认 http://<OPENOS_ADDR>，开发模式可指
http://localhost:5173）。
EOF
}

COMMAND="${1:-}"
TARGET="${2:-desktop}"

case "${COMMAND}" in
  start)
    [ $# -le 2 ] || { usage >&2; exit 1; }
    case "${TARGET}" in
      desktop)
        start_server
        start_desktop
        ;;
      server) start_server ;;
      *) usage >&2; exit 1 ;;
    esac
    ;;
  stop)
    [ $# -le 2 ] || { usage >&2; exit 1; }
    case "${TARGET}" in
      desktop)
        stop_desktop
        stop_server
        ;;
      server) stop_server ;;
      *) usage >&2; exit 1 ;;
    esac
    ;;
  restart)
    [ $# -le 2 ] || { usage >&2; exit 1; }
    case "${TARGET}" in
      desktop)
        stop_desktop
        stop_server
        start_server
        start_desktop
        ;;
      server)
        stop_server
        start_server
        ;;
      *) usage >&2; exit 1 ;;
    esac
    ;;
  -h|--help|help) usage ;;
  *) usage >&2; exit 1 ;;
esac
