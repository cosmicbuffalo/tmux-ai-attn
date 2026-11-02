#!/usr/bin/env bash
# Run tmux-ai-attn-sync against a throwaway tmux server and check that it
# never changes an option outside @ai_attn_: neither while syncing, nor when
# it clears its options on SIGTERM or with --clear. v0.4.0 blanked the user's
# status-format, window formats, and pane-border-format on shutdown.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
socket="$tmp_dir/tmux.sock"
bin="$tmp_dir/tmux-ai-attn-sync"
state_dir="$tmp_dir/state"
daemon_pid=""

tmux_test() {
  env -u TMUX tmux -S "$socket" "$@"
}

cleanup() {
  if [ -n "$daemon_pid" ]; then
    kill -9 "$daemon_pid" 2>/dev/null || true
  fi
  tmux_test kill-server 2>/dev/null || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

fail() {
  echo "$1" >&2
  shift
  for detail in "$@"; do
    printf '%s\n' "$detail" >&2
  done
  exit 1
}

# Every option outside @ai_attn_, at every scope the daemon reads or writes.
user_options() {
  {
    tmux_test show-options -g
    tmux_test show-options -gw
    tmux_test show-options -s
    for window in $(tmux_test list-windows -a -F '#{window_id}'); do
      tmux_test show-options -w -t "$window" | sed "s/^/$window /"
    done
    for pane in $(tmux_test list-panes -a -F '#{pane_id}'); do
      tmux_test show-options -p -t "$pane" | sed "s/^/$pane /"
    done
  } | grep -v '@ai_attn_' | sort
}

expect_unchanged() {
  local after
  after="$(user_options)"
  if [ "$after" != "$before" ]; then
    fail "$1 changed options outside @ai_attn_:" \
      "$(diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") || true)"
  fi
}

(cd "$repo_root" && go build -o "$bin" ./cmd/tmux-ai-attn-sync)
mkdir -p "$state_dir"

tmux_test -f /dev/null new-session -d -s one -x 120 -y 30
tmux_test new-window -d -t one
# A user's own formats and settings, including the ones the daemon reads.
tmux_test set-option -g status-interval 1
tmux_test set-option -g focus-events on
tmux_test set-option -g status-left "[#S] user-left"
tmux_test set-option -g status-right "user-right %H:%M"
tmux_test set-option -g 'status-format[0]' "#[align=left]#{T:status-left} user-format-0"
tmux_test set-option -g 'status-format[1]' "#[align=centre]user-format-1"
tmux_test set-option -gw window-status-format "#I #{E:@ai_attn_window_segment}#W"
tmux_test set-option -gw window-status-current-format "#I* #{E:@ai_attn_window_segment}#W"
tmux_test set-option -gw pane-border-format "user-border #{pane_index}"
tmux_test set-option -w -t one:1 window-status-format "per-window #W"
tmux_test set-option -g @other_plugin_option keep-me
# The plugin's configuration and some state from an earlier run.
tmux_test set-option -g @ai_attn_cli ai-attn
tmux_test set-option -g @ai_attn_tick_ms 120
tmux_test set-option -g @ai_attn_spinner_frame x
tmux_test set-option -w -t one:0 @ai_attn_window_segment stale

before="$(user_options)"

# 1. --clear removes the plugin's state and nothing else.
"$bin" --tmux-socket "$socket" --clear
expect_unchanged "--clear"
[ "$(tmux_test show-option -gqv @ai_attn_spinner_frame)" = "" ] ||
  fail "--clear left @ai_attn_spinner_frame set"
[ "$(tmux_test show-option -wqv -t one:0 @ai_attn_window_segment)" = "" ] ||
  fail "--clear left @ai_attn_window_segment set"
[ "$(tmux_test show-option -gqv @ai_attn_tick_ms)" = "120" ] ||
  fail "--clear removed the @ai_attn_tick_ms setting"

# 2. A running daemon syncs, then clears on SIGTERM, leaving user options alone.
AI_ATTN_STATE_DIR="$state_dir" "$bin" --tmux-socket "$socket" \
  --pidfile "$tmp_dir/daemon.pid" >"$tmp_dir/daemon.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 50); do
  [ -n "$(tmux_test show-option -gqv @ai_attn_state_hash 2>/dev/null)" ] && break
  sleep 0.1
done
sleep 1.5
expect_unchanged "syncing"
kill -TERM "$daemon_pid"
for _ in $(seq 1 50); do
  kill -0 "$daemon_pid" 2>/dev/null || break
  sleep 0.1
done
kill -0 "$daemon_pid" 2>/dev/null && fail "daemon did not exit on SIGTERM"
daemon_pid=""
expect_unchanged "shutdown on SIGTERM"

echo "daemon option tests passed"
