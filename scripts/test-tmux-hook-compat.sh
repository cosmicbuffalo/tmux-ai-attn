#!/usr/bin/env bash
# Verify clear-legacy-hooks.sh against both show-hooks output formats: the
# indexed "event[N] command" of tmux 3.5+ and the bare "event command" older
# versions print for a single hook.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
plugin_dir="$tmp_dir/plugin"
fake_bin="$tmp_dir/bin"
tmux_log="$tmp_dir/tmux.log"
hooks_file="$tmp_dir/hooks"

cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

mkdir -p "$plugin_dir/scripts" "$fake_bin"
cp "$repo_root/scripts/clear-legacy-hooks.sh" "$plugin_dir/scripts/"

cat > "$fake_bin/tmux" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  show-hooks) cat "$FAKE_TMUX_HOOKS" ;;
  *) printf '%s\n' "$*" >> "$FAKE_TMUX_LOG" ;;
esac
EOF
chmod +x "$fake_bin/tmux"

cat > "$hooks_file" <<EOF
client-attached[0] run-shell 'echo user-hook'
client-attached[1] run-shell -b '$plugin_dir/scripts/start-loop.sh $plugin_dir'
client-attached[50] run-shell -b "if [ -x \"$plugin_dir/scripts/start-loop.sh\" ]; then :; fi"
session-created run-shell -b '$plugin_dir/scripts/start-loop.sh $plugin_dir'
alert-bell[0] run-shell 'echo user-bell'
alert-bell[1] run-shell -b '$plugin_dir/scripts/bell-to-ai-attn.sh'
pane-focus-in[0] run-shell '$plugin_dir/scripts/start-loop.sh'
EOF
: > "$tmux_log"

FAKE_TMUX_HOOKS="$hooks_file" FAKE_TMUX_LOG="$tmux_log" PATH="$fake_bin:$PATH" \
  bash "$plugin_dir/scripts/clear-legacy-hooks.sh" "$plugin_dir" 50

expected="$(printf '%s\n' \
  'set-hook -gu client-attached[1]' \
  'set-hook -gu session-created' \
  'set-hook -gu alert-bell[1]')"
if [ "$(cat "$tmux_log")" != "$expected" ]; then
  echo "expected exactly the legacy plugin hooks to be unset; got:" >&2
  cat "$tmux_log" >&2
  exit 1
fi

echo "tmux hook compatibility tests passed"
