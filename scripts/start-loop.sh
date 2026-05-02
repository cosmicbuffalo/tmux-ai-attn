#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="${1:?current dir required}"
SOCKET="$(tmux display-message -p '#{socket_path}' 2>/dev/null || true)"

if [ -z "$SOCKET" ]; then
  exit 0
fi

# Check that ai-attn is available before launching the daemon.
AI_ATTN_CLI="$(tmux show-option -gqv @ai_attn_cli 2>/dev/null || true)"
AI_ATTN_CLI="${AI_ATTN_CLI:-ai-attn}"
if ! command -v "$AI_ATTN_CLI" >/dev/null 2>&1; then
  tmux set-option -gq @ai_attn_last_error \
    "ai-attn not found. Install: curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/ai-attn/main/install.sh | bash"
  exit 0
fi

if ! "$CURRENT_DIR/scripts/ensure-binary.sh" "$CURRENT_DIR"; then
  tmux set-option -gq @ai_attn_last_error "failed to prepare tmux-ai-attn-sync"
  exit 0
fi

SOCKET_KEY="$(printf '%s' "$SOCKET" | tr '/' '_' | tr -d ' ')"

umask 077
if [ -n "${XDG_RUNTIME_DIR:-}" ] && [ -d "$XDG_RUNTIME_DIR" ] && [ -w "$XDG_RUNTIME_DIR" ]; then
  RUN_DIR="$XDG_RUNTIME_DIR/tmux-ai-attn"
else
  RUN_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn/run"
fi
mkdir -p "$RUN_DIR"
chmod 700 "$RUN_DIR" 2>/dev/null || true

PIDFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.pid"
LOCKFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.lock"

# Use flock to prevent concurrent hook invocations from launching duplicate daemons.
exec 9>"$LOCKFILE"
if ! flock -n 9; then
  # Another start-loop.sh is already running; let it handle the launch.
  exit 0
fi

if [ -f "$PIDFILE" ]; then
  PID="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    exit 0
  fi
  rm -f "$PIDFILE"
fi

LOGFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.log"
BIN="$CURRENT_DIR/bin/tmux-ai-attn-sync"

# Clear stale runtime options from a previous daemon crash (kill -9, OOM, etc.).
# Configuration options are preserved; only runtime state is cleared.
"$BIN" --tmux-socket "$SOCKET" --clear 2>/dev/null || true

nohup "$BIN" --tmux-socket "$SOCKET" --pidfile "$PIDFILE" >"$LOGFILE" 2>&1 &
