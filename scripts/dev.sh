#!/usr/bin/env bash
# 本地开发环境启动/停止脚本。用法见 docs/本地开发启动.md
#   scripts/dev.sh start | stop [--with-db] | restart | status | logs [backend|frontend]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND_DIR="$ROOT/backend"
FRONTEND_DIR="$ROOT/frontend"
RUN_DIR="$BACKEND_DIR/data/local"
ENV_FILE="$BACKEND_DIR/.env.local"

PG_CONTAINER="sub2api-local-postgres"
REDIS_CONTAINER="sub2api-local-redis"
BACKEND_PORT="${BACKEND_PORT:-8080}"
FRONTEND_PORT="${FRONTEND_PORT:-3000}"
# vite.config.ts 默认监听 0.0.0.0，这里默认只开放本机；需要局域网访问时设 FRONTEND_HOST=0.0.0.0
FRONTEND_HOST="${FRONTEND_HOST:-127.0.0.1}"
# 终端直连 cdn.sheetjs.com 会超时，安装前端依赖时走本机代理
INSTALL_PROXY="${INSTALL_PROXY:-http://127.0.0.1:7897}"

BACKEND_PID="$RUN_DIR/backend.pid"
FRONTEND_PID="$RUN_DIR/frontend.pid"
BACKEND_LOG="$RUN_DIR/backend.log"
FRONTEND_LOG="$RUN_DIR/frontend.log"

info() { printf '\033[32m[dev]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[dev]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[dev]\033[0m %s\n' "$*" >&2; exit 1; }

port_pid() { lsof -nP -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -1; }

# 优先用 pid 文件，失效时按端口找
find_pid() {
  local pidfile="$1" port="$2" pid=""
  [ -f "$pidfile" ] && pid="$(cat "$pidfile" 2>/dev/null)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then echo "$pid"; return; fi
  port_pid "$port"
}

wait_for() { # wait_for <秒> <命令...>
  local secs="$1"; shift
  local i=0
  while [ "$i" -lt "$secs" ]; do
    "$@" >/dev/null 2>&1 && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}

start_db() {
  command -v docker >/dev/null || die "找不到 docker"
  docker info >/dev/null 2>&1 || die "Docker 没有运行，请先打开 Docker Desktop"
  local c
  for c in "$PG_CONTAINER" "$REDIS_CONTAINER"; do
    docker inspect "$c" >/dev/null 2>&1 || die "容器 $c 不存在"
    if [ "$(docker inspect -f '{{.State.Running}}' "$c")" != "true" ]; then
      info "启动容器 $c"
      docker start "$c" >/dev/null || die "容器 $c 启动失败"
    fi
  done
  wait_for 30 docker exec "$PG_CONTAINER" pg_isready -U sub2api || die "PostgreSQL 30 秒内没有就绪"
  info "PostgreSQL / Redis 已就绪"
}

start_backend() {
  local pid; pid="$(find_pid "$BACKEND_PID" "$BACKEND_PORT")"
  if [ -n "$pid" ]; then info "后端已在运行 (pid $pid)"; return; fi
  [ -f "$ENV_FILE" ] || die "缺少 $ENV_FILE"
  if [ ! -x "$BACKEND_DIR/bin/server" ]; then
    info "没有 bin/server，开始编译后端"
    (cd "$BACKEND_DIR" && make build) || die "后端编译失败"
  fi
  info "启动后端 (端口 $BACKEND_PORT)"
  (
    cd "$BACKEND_DIR" || exit 1
    set -a; . "$ENV_FILE"; set +a
    nohup ./bin/server >>"$BACKEND_LOG" 2>&1 &
    echo $! >"$BACKEND_PID"
  )
  if wait_for 60 curl -sf "http://127.0.0.1:$BACKEND_PORT/health"; then
    info "后端已启动 http://127.0.0.1:$BACKEND_PORT"
  else
    tail -20 "$BACKEND_LOG"
    die "后端 60 秒内没有通过健康检查，日志见 $BACKEND_LOG"
  fi
}

ensure_frontend_deps() {
  [ -x "$FRONTEND_DIR/node_modules/.bin/vite" ] && return
  warn "frontend/node_modules 不完整，用 pnpm 9 重新安装（不会修改 pnpm-lock.yaml）"
  (cd "$ROOT" && HTTPS_PROXY="$INSTALL_PROXY" HTTP_PROXY="$INSTALL_PROXY" \
    npx -y pnpm@9 --dir frontend install --frozen-lockfile) || die "前端依赖安装失败"
}

start_frontend() {
  local pid; pid="$(find_pid "$FRONTEND_PID" "$FRONTEND_PORT")"
  if [ -n "$pid" ]; then info "前端已在运行 (pid $pid)"; return; fi
  ensure_frontend_deps
  info "启动前端 (端口 $FRONTEND_PORT)"
  # 直接调用 vite，绕开系统 pnpm 11 在 run 前自动重装依赖的问题
  (
    cd "$FRONTEND_DIR" || exit 1
    VITE_DEV_PORT="$FRONTEND_PORT" nohup ./node_modules/.bin/vite --host "$FRONTEND_HOST" >"$FRONTEND_LOG" 2>&1 &
    echo $! >"$FRONTEND_PID"
  )
  if wait_for 60 curl -sf -o /dev/null "http://127.0.0.1:$FRONTEND_PORT/"; then
    info "前端已启动 http://localhost:$FRONTEND_PORT"
  else
    tail -20 "$FRONTEND_LOG"
    die "前端 60 秒内没有响应，日志见 $FRONTEND_LOG"
  fi
}

stop_one() { # stop_one <名称> <pid文件> <端口>
  local name="$1" pidfile="$2" port="$3" pid
  pid="$(find_pid "$pidfile" "$port")"
  if [ -z "$pid" ]; then info "$name 没有运行"; rm -f "$pidfile"; return; fi
  info "停止$name (pid $pid)"
  kill "$pid" 2>/dev/null
  if ! wait_for 15 sh -c "! kill -0 $pid 2>/dev/null"; then
    warn "$name 15 秒内没有退出，强制结束"
    kill -9 "$pid" 2>/dev/null
  fi
  rm -f "$pidfile"
}

stop_db() {
  info "停止容器 $PG_CONTAINER $REDIS_CONTAINER"
  docker stop "$PG_CONTAINER" "$REDIS_CONTAINER" >/dev/null
}

status() {
  local pid
  pid="$(find_pid "$BACKEND_PID" "$BACKEND_PORT")"
  if [ -n "$pid" ]; then
    printf '后端      运行中 pid %-7s http://127.0.0.1:%s  health=%s\n' "$pid" "$BACKEND_PORT" \
      "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$BACKEND_PORT/health")"
  else
    echo "后端      未运行"
  fi
  pid="$(find_pid "$FRONTEND_PID" "$FRONTEND_PORT")"
  if [ -n "$pid" ]; then
    printf '前端      运行中 pid %-7s http://localhost:%s\n' "$pid" "$FRONTEND_PORT"
  else
    echo "前端      未运行"
  fi
  local c
  for c in "$PG_CONTAINER" "$REDIS_CONTAINER"; do
    printf '%-24s %s\n' "$c" "$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null || echo '不存在')"
  done
}

cmd="${1:-}"
mkdir -p "$RUN_DIR"
case "$cmd" in
  start)   start_db; start_backend; start_frontend; echo; status ;;
  stop)    stop_one "前端" "$FRONTEND_PID" "$FRONTEND_PORT"
           stop_one "后端" "$BACKEND_PID" "$BACKEND_PORT"
           [ "${2:-}" = "--with-db" ] && stop_db
           true ;;
  restart) "$0" stop; "$0" start ;;
  status)  status ;;
  logs)    case "${2:-backend}" in
             backend)  tail -f "$BACKEND_LOG" ;;
             frontend) tail -f "$FRONTEND_LOG" ;;
             *) die "logs 只支持 backend 或 frontend" ;;
           esac ;;
  *) echo "用法: $0 {start|stop [--with-db]|restart|status|logs [backend|frontend]}"; exit 1 ;;
esac
