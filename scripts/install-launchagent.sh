#!/usr/bin/env bash
# Install DB Compare as a user LaunchAgent (auto-restart, survives terminal close).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LABEL="com.pp.dbcompare.server"
PLIST_SRC="$ROOT/scripts/com.pp.dbcompare.server.plist.template"
PLIST_DST="$HOME/Library/LaunchAgents/${LABEL}.plist"

if [[ ! -x "$ROOT/bin/dbcompare-server" ]]; then
  echo "missing $ROOT/bin/dbcompare-server — run: make build-server"
  exit 1
fi

mkdir -p "$HOME/Library/LaunchAgents"
sed "s|__ROOT__|$ROOT|g" "$PLIST_SRC" >"$PLIST_DST"

# Prefer launchd when available (Terminal.app / login session). Cursor agent shells often fail bootstrap.
UID_NUM="$(id -u)"
DOMAIN="gui/${UID_NUM}"
launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
if ! launchctl bootstrap "$DOMAIN" "$PLIST_DST" 2>/tmp/dbcompare-launchctl.err; then
  echo "launchctl bootstrap failed (common inside Cursor)."
  echo "Falling back to detached daemon via run-server.sh ..."
  "$ROOT/scripts/run-server.sh" restart
  echo "For auto-restart on login, run this same command in Terminal.app:"
  echo "  make serve-daemon"
  exit 0
fi
launchctl enable "$DOMAIN/$LABEL" 2>/dev/null || true
launchctl kickstart -k "$DOMAIN/$LABEL"

sleep 1
if curl -sf "http://127.0.0.1:${DBCOMPARE_PORT:-13880}/" >/dev/null; then
  echo "LaunchAgent installed and running: http://127.0.0.1:${DBCOMPARE_PORT:-13880}/"
  echo "plist: $PLIST_DST"
  echo "log:  $ROOT/.dbcompare-server.log"
else
  echo "LaunchAgent installed but health check failed; see $ROOT/.dbcompare-server.log"
  exit 1
fi
