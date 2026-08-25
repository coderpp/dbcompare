#!/usr/bin/env bash
# Start/stop DB Compare HTTP server (Wails server mode) in the background.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PIDFILE="$ROOT/.dbcompare-server.pid"
LOG="$ROOT/.dbcompare-server.log"
BIN="$ROOT/bin/dbcompare-server"
PORT="${DBCOMPARE_PORT:-13880}"
URL="http://127.0.0.1:${PORT}/"

running_pid() {
  if [[ -f "$PIDFILE" ]]; then
    local pid
    pid="$(cat "$PIDFILE")"
    if kill -0 "$pid" 2>/dev/null; then
      echo "$pid"
      return 0
    fi
    rm -f "$PIDFILE"
  fi
  return 1
}

cmd_start() {
  if pid="$(running_pid)"; then
    echo "dbcompare-server already running (pid=$pid) $URL"
    exit 0
  fi
  if lsof -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $PORT already in use; stop the other process first"
    exit 1
  fi
  if [[ ! -x "$BIN" ]]; then
    echo "missing $BIN — run: make build-server"
    exit 1
  fi
  cd "$ROOT"
  nohup "$BIN" >>"$LOG" 2>&1 &
  local pid=$!
  disown "$pid" 2>/dev/null || true
  echo "$pid" >"$PIDFILE"
  for _ in 1 2 3 4 5; do
    sleep 1
    if kill -0 "$pid" 2>/dev/null && curl -sf "$URL" >/dev/null; then
      echo "dbcompare-server started (pid=$pid) $URL"
      echo "log: $LOG"
      return 0
    fi
  done
  echo "failed to start; see $LOG"
  rm -f "$PIDFILE"
  exit 1
}

cmd_stop() {
  if pid="$(running_pid)"; then
    kill "$pid" 2>/dev/null || true
    sleep 1
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$PIDFILE"
    echo "stopped pid=$pid"
  else
    pkill -f 'bin/dbcompare-server' 2>/dev/null || true
    echo "dbcompare-server was not running"
  fi
}

cmd_status() {
  if pid="$(running_pid)"; then
    echo "running pid=$pid $URL"
    curl -sf -o /dev/null -w "http=%{http_code}\n" "$URL" || true
  else
    echo "not running"
    exit 1
  fi
}

cmd_restart() {
  cmd_stop || true
  cmd_start
}

case "${1:-start}" in
  start) cmd_start ;;
  stop) cmd_stop ;;
  status) cmd_status ;;
  restart) cmd_restart ;;
  *)
    echo "usage: $0 {start|stop|status|restart}"
    exit 1
    ;;
esac
