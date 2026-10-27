#!/usr/bin/env bash
# Load tmux-ai-attn.tmux into a throwaway tmux server and check what the
# compiled tmux-ai-attn.conf leaves behind: option defaults, the sync loop
# start, and hooks that hold the plugin's absolute path and work when fired.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
plugin_dir="$tmp_dir/plugin"
socket="$tmp_dir/tmux.sock"
loop_log="$tmp_dir/start-loop.log"
hook_log="$tmp_dir/hooks.log"

tmux_test() {
  env -u TMUX tmux -S "$socket" "$@"
}

cleanup() {
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

# Wait for a background run-shell to write a line matching $2 to file $1.
wait_for_line() {
  local _i
  for _i in $(seq 1 50); do
    grep -Fq -- "$2" "$1" 2>/dev/null && return 0
    sleep 0.1
  done
  return 1
}

mkdir -p "$plugin_dir/scripts"
cp "$repo_root/tmux-ai-attn.tmux" "$repo_root/tmux-ai-attn.conf" "$plugin_dir/"
cat > "$plugin_dir/scripts/start-loop.sh" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$loop_log"
EOF
chmod +x "$plugin_dir/tmux-ai-attn.tmux" "$plugin_dir/scripts/start-loop.sh"
: > "$loop_log"

# The hooks' shell reads TMUX_AI_ATTN_LOG from the server's environment.
TMUX_AI_ATTN_LOG="$hook_log" tmux_test -f /dev/null new-session -d -s main
tmux_test set-option -g @ai_attn_seen_flash_seconds 30
# A stopped daemon leaves '' behind; the default must replace it.
tmux_test set-option -g @ai_attn_cli ''
tmux_test set-hook -ag client-attached "run-shell 'echo user-hook'"

load_plugin() {
  TMUX="$socket,0,0" bash "$plugin_dir/tmux-ai-attn.tmux"
}
load_plugin
load_plugin

assert_option() {
  local actual
  actual="$(tmux_test show-option -gqv "$1")"
  [ "$actual" = "$2" ] || fail "$1: expected '$2', got '$actual'"
}
assert_option @ai_attn_seen_flash_seconds 30
assert_option @ai_attn_cli ai-attn
assert_option @ai_attn_color_working '#d88786'
assert_option @ai_attn_spinner_frames '·,·,✢,✳,✶,✽,✻,✻,✻,✽,✶,✳,✢,·'
assert_option @ai_attn_enable_default_formats off

wait_for_line "$loop_log" "$plugin_dir --restart" \
  || fail "loading the plugin did not restart the sync loop" "$(cat "$loop_log")"

hooks="$(tmux_test show-hooks -g)"
for event in client-attached client-session-changed session-created after-new-session; do
  count="$(printf '%s\n' "$hooks" | grep -c "^$event\[.*$plugin_dir/scripts/start-loop.sh" || true)"
  [ "$count" -eq 1 ] || fail "$event: expected one plugin hook after two loads, found $count" "$hooks"
done
printf '%s\n' "$hooks" | grep -Fq "echo user-hook" \
  || fail "the user's client-attached hook was lost" "$hooks"
if printf '%s\n' "$hooks" | grep -Fq 'TMUX_AI_ATTN_DIR'; then
  fail "a hook kept \$TMUX_AI_ATTN_DIR instead of the plugin path" "$hooks"
fi

# Firing a hook runs start-loop.sh from the plugin directory...
: > "$loop_log"
tmux_test new-session -d -s second
wait_for_line "$loop_log" "$plugin_dir" \
  || fail "session-created did not run start-loop.sh" "$(cat "$loop_log")"

# ...and logs, without failing, once the plugin is gone.
chmod -x "$plugin_dir/scripts/start-loop.sh"
tmux_test new-session -d -s third
wait_for_line "$hook_log" "[hook] plugin missing: $plugin_dir/scripts/start-loop.sh" \
  || fail "a hook did not log the missing plugin" "$(cat "$hook_log" 2>/dev/null)"
grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8} \[hook\]' "$hook_log" \
  || fail "the hook log line has no fire-time timestamp" "$(cat "$hook_log")"
chmod +x "$plugin_dir/scripts/start-loop.sh"

tmux_test set-option -g @ai_attn_enable_default_formats on
load_plugin
assert_option status-interval 1
tmux_test show-option -gwqv window-status-format | grep -Fq '@ai_attn_window_flash' \
  || fail "default formats were not installed"

echo "tmux conf tests passed"
