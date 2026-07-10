#!/usr/bin/env bash
# Verify reload-safe hook handling against the output format used before tmux 3.5.
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
cp "$repo_root/tmux-ai-attn.tmux" "$plugin_dir/tmux-ai-attn.tmux"

cat > "$plugin_dir/scripts/start-loop.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat > "$plugin_dir/scripts/bell-to-ai-attn.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat > "$fake_bin/tmux" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  show-options)
    exit 0
    ;;
  show-option)
    if [ "${*: -1}" = "@ai_attn_enable_default_formats" ]; then
      printf 'off\n'
    fi
    exit 0
    ;;
  show-hooks)
    cat "$FAKE_TMUX_HOOKS"
    exit 0
    ;;
esac

printf '%s' "${1:-}" >> "$FAKE_TMUX_LOG"
shift || true
for arg in "$@"; do
  printf '\t%s' "$arg" >> "$FAKE_TMUX_LOG"
done
printf '\n' >> "$FAKE_TMUX_LOG"
EOF

chmod +x \
  "$plugin_dir/tmux-ai-attn.tmux" \
  "$plugin_dir/scripts/start-loop.sh" \
  "$plugin_dir/scripts/bell-to-ai-attn.sh" \
  "$fake_bin/tmux"

cat > "$hooks_file" <<EOF
client-attached run-shell 'echo user-hook'
client-attached run-shell -b '$plugin_dir/scripts/start-loop.sh $plugin_dir'
EOF
: > "$tmux_log"

FAKE_TMUX_HOOKS="$hooks_file" \
FAKE_TMUX_LOG="$tmux_log" \
PATH="$fake_bin:$PATH" \
  bash "$plugin_dir/tmux-ai-attn.tmux"

expected=$(printf "set-hook\t-ag\tclient-attached\trun-shell 'echo user-hook'")
if ! grep -Fqx "$expected" "$tmux_log"; then
  echo "older unindexed user hook was not preserved during reload" >&2
  cat "$tmux_log" >&2
  exit 1
fi

echo "tmux hook compatibility tests passed"
