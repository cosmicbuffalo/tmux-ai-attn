#!/usr/bin/env bash
# Focused regression tests for portable daemon launch and PID ownership.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
plugin_dir="$tmp_dir/plugin"
fake_bin="$tmp_dir/fake-bin"
run_dir="$tmp_dir/cache/tmux-ai-attn/run"
daemon_log="$tmp_dir/daemons"
tmux_log="$tmp_dir/tmux-calls"
unrelated_pid=""

cleanup() {
  local pidfile pid
  if [ -d "$run_dir" ]; then
    for pidfile in "$run_dir"/*.pid; do
      [ -e "$pidfile" ] || continue
      pid="$(cat "$pidfile" 2>/dev/null || true)"
      if [[ "$pid" =~ ^[0-9]+$ ]]; then
        kill "$pid" 2>/dev/null || true
      fi
    done
  fi
  if [ -n "$unrelated_pid" ]; then
    kill "$unrelated_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

mkdir -p "$plugin_dir/scripts" "$plugin_dir/bin" "$fake_bin"
cp "$repo_root/scripts/start-loop.sh" "$plugin_dir/scripts/start-loop.sh"

cat > "$plugin_dir/scripts/ensure-binary.sh" <<'ENSURE_BINARY'
#!/usr/bin/env bash
exit 0
ENSURE_BINARY

cat > "$plugin_dir/bin/tmux-ai-attn-sync" <<'FAKE_DAEMON'
#!/usr/bin/env bash
set -euo pipefail

socket=""
pidfile=""
clear=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tmux-socket) socket="$2"; shift 2 ;;
    --pidfile) pidfile="$2"; shift 2 ;;
    --clear) clear=1; shift ;;
    *) shift ;;
  esac
done
if [ "$clear" -eq 1 ]; then
  exit 0
fi

printf '%s\n' "$$" > "$pidfile"
printf '%s\t%s\n' "$socket" "$$" >> "$AI_ATTN_DAEMON_LOG"
remove_pidfile() {
  rm -f "$pidfile"
}
trap remove_pidfile EXIT
trap 'exit 0' HUP INT TERM
while :; do
  sleep 1
done
FAKE_DAEMON

cat > "$fake_bin/tmux" <<'FAKE_TMUX'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  display-message) printf '%s\n' "$FAKE_TMUX_SOCKET" ;;
  show-option) printf '%s\n' "$FAKE_AI_ATTN" ;;
  set-option) printf '%s\n' "$*" >> "$FAKE_TMUX_LOG" ;;
  *) exit 1 ;;
esac
FAKE_TMUX

cat > "$fake_bin/ai-attn" <<'FAKE_AI_ATTN'
#!/usr/bin/env bash
if [ "${1:-}" = "--version" ]; then
  echo "ai-attn v0.3.2"
fi
exit 0
FAKE_AI_ATTN

cat > "$fake_bin/ai-attn-old" <<'FAKE_AI_ATTN_OLD'
#!/usr/bin/env bash
if [ "${1:-}" = "--version" ]; then
  echo "ai-attn v0.3.1"
fi
exit 0
FAKE_AI_ATTN_OLD

chmod +x \
  "$plugin_dir/scripts/start-loop.sh" \
  "$plugin_dir/scripts/ensure-binary.sh" \
  "$plugin_dir/bin/tmux-ai-attn-sync" \
  "$fake_bin/tmux" \
  "$fake_bin/ai-attn" \
  "$fake_bin/ai-attn-old"
: > "$daemon_log"
: > "$tmux_log"

socket_key() {
  printf '%s' "$1" | cksum | awk '{print $1 "-" $2}'
}

pidfile_for() {
  printf '%s/tmux-ai-attn-%s.pid\n' "$run_dir" "$(socket_key "$1")"
}

start_loop() {
  local ai_attn="${TEST_AI_ATTN:-$fake_bin/ai-attn}"
  FAKE_TMUX_SOCKET="$1" \
  FAKE_AI_ATTN="$ai_attn" \
  FAKE_TMUX_LOG="$tmux_log" \
  AI_ATTN_DAEMON_LOG="$daemon_log" \
  XDG_RUNTIME_DIR='' \
  XDG_CACHE_HOME="$tmp_dir/cache" \
  PATH="$fake_bin:$PATH" \
    bash "$plugin_dir/scripts/start-loop.sh" "$plugin_dir" "${2:-}"
}

assert_alive() {
  if ! kill -0 "$1" 2>/dev/null; then
    echo "expected pid $1 to be alive" >&2
    exit 1
  fi
}

socket_a="/tmp/tmux/a/b"
socket_b="/tmp/tmux/a_b"

start_loop "$socket_a"
pid_a="$(cat "$(pidfile_for "$socket_a")")"
assert_alive "$pid_a"

# A normal hook invocation must reuse the daemon instead of duplicating it.
start_loop "$socket_a"
test "$(cat "$(pidfile_for "$socket_a")")" = "$pid_a"

# Paths that collided under slash-to-underscore conversion now stay distinct.
test "$(socket_key "$socket_a")" != "$(socket_key "$socket_b")"

# A stale PID file pointing at an unrelated live process must never be signaled.
sleep 30 &
unrelated_pid=$!
mkdir -p "$run_dir"
printf '%s\n' "$unrelated_pid" > "$(pidfile_for "$socket_b")"
start_loop "$socket_b" --restart
assert_alive "$unrelated_pid"
pid_b="$(cat "$(pidfile_for "$socket_b")")"
assert_alive "$pid_b"
test "$pid_b" != "$unrelated_pid"

# A restart replaces a daemon that owns the PID file and exact tmux socket.
start_loop "$socket_a" --restart
new_pid_a="$(cat "$(pidfile_for "$socket_a")")"
assert_alive "$new_pid_a"
test "$new_pid_a" != "$pid_a"
if kill -0 "$pid_a" 2>/dev/null; then
  echo "old daemon pid $pid_a survived restart" >&2
  exit 1
fi

# Concurrent hooks serialize through the portable directory lock.
socket_c="/tmp/tmux/concurrent"
launch_pids=()
for _i in 1 2 3 4 5; do
  start_loop "$socket_c" &
  launch_pids+=("$!")
done
for pid in "${launch_pids[@]}"; do
  wait "$pid"
done
test "$(grep -Fc "$socket_c" "$daemon_log")" = "1"

# The plugin requires the socket-keyed ai-attn release before launching.
socket_old="/tmp/tmux/old-ai-attn"
TEST_AI_ATTN="$fake_bin/ai-attn-old" start_loop "$socket_old"
test ! -e "$(pidfile_for "$socket_old")"
grep -Fq "ai-attn >= v0.3.2 required" "$tmux_log"

echo "start-loop tests passed"
