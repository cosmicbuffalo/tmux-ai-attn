#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="${1:?current dir required}"
RESTART=0
if [ "${2:-}" = "--restart" ]; then
  RESTART=1
fi
SOCKET="$(tmux display-message -p '#{socket_path}' 2>/dev/null || true)"

if [ -z "$SOCKET" ]; then
  exit 0
fi

# Check that ai-attn is available before launching the daemon.
# When missing, optionally auto-install it via the pinned installer if the
# user has explicitly opted in by setting @ai_attn_auto_install = "on".
AI_ATTN_CLI="$(tmux show-option -gqv @ai_attn_cli 2>/dev/null || true)"
AI_ATTN_CLI="${AI_ATTN_CLI:-ai-attn}"
AI_ATTN_INSTALL_URL="https://raw.githubusercontent.com/cosmicbuffalo/ai-attn/v0.2.0/install.sh"
INSTALL_HINT="ai-attn not found. Install: curl -fsSL ${AI_ATTN_INSTALL_URL} | bash (or set -g @ai_attn_auto_install \"on\")"
if ! command -v "$AI_ATTN_CLI" >/dev/null 2>&1; then
  AUTO_INSTALL="$(tmux show-option -gqv @ai_attn_auto_install 2>/dev/null || true)"
  AUTO_INSTALL="${AUTO_INSTALL:-off}"
  if [ "$AUTO_INSTALL" = "on" ]; then
    if ! command -v curl >/dev/null 2>&1; then
      tmux set-option -gq @ai_attn_last_error \
        "ai-attn auto-install requires curl; install curl or install ai-attn manually"
      exit 0
    fi
    # The script runs under `set -euo pipefail`. A failing curl or bash would
    # otherwise kill the script before we get to surface @ai_attn_last_error,
    # so neutralize errexit/pipefail around the install pipeline and restore
    # them afterward.
    set +e
    install_log="$(curl -fsSL "$AI_ATTN_INSTALL_URL" 2>&1 | bash 2>&1)"
    install_status=$?
    set -e
    if [ "$install_status" -ne 0 ] || ! command -v "$AI_ATTN_CLI" >/dev/null 2>&1; then
      # Keep the error single-line for tmux's display-message; truncate noise.
      first_err="$(printf '%s' "$install_log" | tr '\n' ' ' | cut -c 1-200)"
      tmux set-option -gq @ai_attn_last_error \
        "ai-attn auto-install failed${first_err:+: }${first_err}"
      exit 0
    fi
  else
    tmux set-option -gq @ai_attn_last_error "$INSTALL_HINT"
    exit 0
  fi
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
    if [ "$RESTART" -eq 0 ]; then
      exit 0
    fi
    kill "$PID" 2>/dev/null || true
    for _i in 1 2 3 4 5; do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.1
    done
  fi
  rm -f "$PIDFILE"
fi

LOGFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.log"
BIN="$CURRENT_DIR/bin/tmux-ai-attn-sync"

# Clear stale runtime options from a previous daemon crash (kill -9, OOM, etc.).
# Configuration options are preserved; only runtime state is cleared.
"$BIN" --tmux-socket "$SOCKET" --clear 2>/dev/null || true

nohup "$BIN" --tmux-socket "$SOCKET" --pidfile "$PIDFILE" >"$LOGFILE" 2>&1 9>&- &
