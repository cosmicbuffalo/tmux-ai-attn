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

SOCKET_KEY="$(printf '%s' "$SOCKET" | cksum | awk '{print $1 "-" $2}')"
LEGACY_SOCKET_KEY="$(printf '%s' "$SOCKET" | tr '/' '_' | tr -d ' ')"

umask 077
if [ -n "${XDG_RUNTIME_DIR:-}" ] && [ -d "$XDG_RUNTIME_DIR" ] && [ -w "$XDG_RUNTIME_DIR" ]; then
  RUN_DIR="$XDG_RUNTIME_DIR/tmux-ai-attn"
else
  RUN_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn/run"
fi
mkdir -p "$RUN_DIR"
chmod 700 "$RUN_DIR" 2>/dev/null || true

PIDFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.pid"
LEGACY_PIDFILE="${RUN_DIR}/tmux-ai-attn-${LEGACY_SOCKET_KEY}.pid"
LOCKDIR="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.lock.d"
LOCK_OWNER="$LOCKDIR/owner"
LOCK_HELD=0

process_command() {
  ps -ww -p "$1" -o command= 2>/dev/null || true
}

pid_is_launcher() {
  local pid="$1" command
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  command="$(process_command "$pid")"
  case "$command" in
    *start-loop.sh*) return 0 ;;
  esac
  return 1
}

pid_is_daemon() {
  local pid="$1" command
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  command="$(process_command "$pid")"
  case "$command" in
    *tmux-ai-attn-sync*"--tmux-socket $SOCKET"*) return 0 ;;
  esac
  return 1
}

# Invoked indirectly by trap after acquire_lock succeeds.
# shellcheck disable=SC2317
release_lock() {
  local owner
  if [ "$LOCK_HELD" -ne 1 ]; then
    return
  fi
  owner="$(cat "$LOCK_OWNER" 2>/dev/null || true)"
  if [ "$owner" = "$$" ]; then
    rm -f "$LOCK_OWNER"
    rmdir "$LOCKDIR" 2>/dev/null || true
  fi
  LOCK_HELD=0
}

acquire_lock() {
  local _attempt owner owner_after
  for _attempt in 1 2 3; do
    if mkdir "$LOCKDIR" 2>/dev/null; then
      LOCK_HELD=1
      trap release_lock EXIT
      trap 'exit 1' HUP INT TERM
      printf '%s\n' "$$" > "$LOCK_OWNER"
      return 0
    fi

    owner="$(cat "$LOCK_OWNER" 2>/dev/null || true)"
    if pid_is_launcher "$owner"; then
      return 1
    fi

    # Give a just-created lock time to publish its owner before treating it as stale.
    sleep 0.05
    owner_after="$(cat "$LOCK_OWNER" 2>/dev/null || true)"
    if [ "$owner" = "$owner_after" ] && ! pid_is_launcher "$owner_after"; then
      rm -f "$LOCK_OWNER"
      rmdir "$LOCKDIR" 2>/dev/null || true
    fi
  done
  return 1
}

if ! acquire_lock; then
  # Another start-loop.sh owns this socket's launch decision.
  exit 0
fi

# Check that ai-attn is available before launching the daemon.
AI_ATTN_CLI="$(tmux show-option -gqv @ai_attn_cli 2>/dev/null || true)"
AI_ATTN_CLI="${AI_ATTN_CLI:-ai-attn}"
REQUIRED_AI_ATTN_VERSION="0.3.2"
if ! command -v "$AI_ATTN_CLI" >/dev/null 2>&1; then
  tmux set-option -gq @ai_attn_last_error \
    "ai-attn not found. Install: curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/ai-attn/v0.3.2/install.sh | bash"
  exit 0
fi

version_at_least() {
  local output="$1" required="$2" regex
  local current_major current_minor current_patch
  local required_major required_minor required_patch
  regex='(^|[^0-9])([0-9]+)\.([0-9]+)\.([0-9]+)'
  if [[ ! "$output" =~ $regex ]]; then
    return 1
  fi
  current_major="${BASH_REMATCH[2]}"
  current_minor="${BASH_REMATCH[3]}"
  current_patch="${BASH_REMATCH[4]}"
  IFS=. read -r required_major required_minor required_patch <<< "$required"
  if (( current_major != required_major )); then
    (( current_major > required_major ))
  elif (( current_minor != required_minor )); then
    (( current_minor > required_minor ))
  else
    (( current_patch >= required_patch ))
  fi
}

AI_ATTN_VERSION_OUTPUT="$("$AI_ATTN_CLI" --version 2>/dev/null || true)"
if ! version_at_least "$AI_ATTN_VERSION_OUTPUT" "$REQUIRED_AI_ATTN_VERSION"; then
  tmux set-option -gq @ai_attn_last_error \
    "ai-attn >= v${REQUIRED_AI_ATTN_VERSION} required (found: ${AI_ATTN_VERSION_OUTPUT:-unknown})"
  exit 0
fi

if ! "$CURRENT_DIR/scripts/ensure-binary.sh" "$CURRENT_DIR"; then
  tmux set-option -gq @ai_attn_last_error "failed to prepare tmux-ai-attn-sync"
  exit 0
fi

for CANDIDATE_PIDFILE in "$PIDFILE" "$LEGACY_PIDFILE"; do
  [ -f "$CANDIDATE_PIDFILE" ] || continue
  PID="$(cat "$CANDIDATE_PIDFILE" 2>/dev/null || true)"
  if pid_is_daemon "$PID"; then
    if [ "$RESTART" -eq 0 ]; then
      exit 0
    fi
    kill "$PID" 2>/dev/null || true
    for _i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$PID" 2>/dev/null; then
      tmux set-option -gq @ai_attn_last_error \
        "timed out stopping tmux-ai-attn-sync pid $PID"
      exit 1
    fi
  fi
  # A live but unrelated PID is deliberately left alone.
  rm -f "$CANDIDATE_PIDFILE"
done

LOGFILE="${RUN_DIR}/tmux-ai-attn-${SOCKET_KEY}.log"
BIN="$CURRENT_DIR/bin/tmux-ai-attn-sync"

# Clear stale runtime options from a previous daemon crash (kill -9, OOM, etc.).
# Configuration options are preserved; only runtime state is cleared.
"$BIN" --tmux-socket "$SOCKET" --clear 2>/dev/null || true

nohup "$BIN" --tmux-socket "$SOCKET" --pidfile "$PIDFILE" >"$LOGFILE" 2>&1 &
DAEMON_PID=$!

# Keep the launch lock until the daemon has published and owns its PID file.
for _i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  PID="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ "$PID" = "$DAEMON_PID" ] && pid_is_daemon "$PID"; then
    exit 0
  fi
  kill -0 "$DAEMON_PID" 2>/dev/null || break
  sleep 0.05
done

tmux set-option -gq @ai_attn_last_error \
  "tmux-ai-attn-sync failed to start (see $LOGFILE)"
exit 1
