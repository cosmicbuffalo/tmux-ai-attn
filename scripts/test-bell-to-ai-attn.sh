#!/usr/bin/env bash
# Focused regression tests for the alert-bell -> ai-attn writer.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

calls="$tmp_dir/calls"
fake_ai_attn="$tmp_dir/ai-attn"

cat > "$fake_ai_attn" <<'FAKE_AI_ATTN'
#!/usr/bin/env bash
set -euo pipefail

seen_socket=0
for arg in "$@"; do
  if [ "$arg" = "--socket" ]; then
    seen_socket=1
  fi
done

for arg in "$@"; do
  printf '%q ' "$arg"
done >> "$AI_ATTN_TEST_CALLS"
printf '\n' >> "$AI_ATTN_TEST_CALLS"

if [ "${AI_ATTN_FAIL_ON_SOCKET:-0}" = "1" ] && [ "$seen_socket" -eq 1 ]; then
  exit 2
fi
exit 0
FAKE_AI_ATTN
chmod +x "$fake_ai_attn"

run_bell() {
  env \
    XDG_STATE_HOME="$tmp_dir/state" \
    AI_ATTN_TEST_CALLS="$calls" \
    "${env_args[@]}" \
    "$repo_root/scripts/bell-to-ai-attn.sh" \
    "$@"
}

assert_line_count() {
  local want="$1" got
  got="$(wc -l < "$calls" | tr -d ' ')"
  if [ "$got" != "$want" ]; then
    echo "expected $want ai-attn calls, got $got" >&2
    cat "$calls" >&2
    exit 1
  fi
}

assert_contains() {
  local needle="$1"
  if ! grep -Fq -- "$needle" "$calls"; then
    echo "expected calls to contain: $needle" >&2
    cat "$calls" >&2
    exit 1
  fi
}

assert_not_contains_line() {
  local line_no="$1" needle="$2" line
  line="$(sed -n "${line_no}p" "$calls")"
  if printf '%s\n' "$line" | grep -Fq -- "$needle"; then
    echo "expected line $line_no not to contain: $needle" >&2
    cat "$calls" >&2
    exit 1
  fi
}

: > "$calls"
env_args=()
run_bell node codex "%15" "/work/project" "$fake_ai_attn" "/tmp/tmux-1000/default"
assert_line_count 1
assert_contains "--socket"
assert_contains "/tmp/tmux-1000/default"

: > "$calls"
env_args=(AI_ATTN_FAIL_ON_SOCKET=1)
run_bell node codex "%15" "/work/project" "$fake_ai_attn" "/tmp/tmux-1000/default"
assert_line_count 2
assert_contains "--socket"
assert_not_contains_line 2 "--socket"

: > "$calls"
env_args=()
run_bell bash bash "%15" "/work/project" "$fake_ai_attn" "/tmp/tmux-1000/default"
assert_line_count 0
