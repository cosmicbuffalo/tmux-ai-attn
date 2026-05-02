#!/usr/bin/env bash
# tmux-ai-attn: alert-bell → ai-attn waiting writer.
#
# Codex's `notify` channel only emits agent-turn-complete in current
# versions, so permission prompts mid-turn aren't reported through the
# usual hook path. Codex does emit an ASCII BEL on permission prompts,
# which tmux surfaces via the alert-bell hook. This script translates
# that bell into an ai-attn `waiting` record so the status line flashes.
#
# Args (all positional, supplied by tmux format expansion):
#   $1  pane_current_command  — current foreground process basename
#   $2  pane_start_command    — original launch command (codex runs under
#                               node, so current_command is "node" but
#                               start_command is "codex")
#   $3  pane_id               — used as the pane_id in the ai-attn record
#   $4  pane_current_path     — recorded as cwd
#   $5  ai-attn CLI path      — typically @ai_attn_cli (defaults to "ai-attn")
set -euo pipefail

pane_command="${1:-}"
pane_start="${2:-}"
pane_id="${3:-}"
pane_path="${4:-}"
ai_attn_cli="${5:-ai-attn}"

# Diagnostic log so we can confirm the hook is firing and see what tmux
# passes through. One line per bell. Truncated at ~5 MB to bound growth.
log_file="${XDG_STATE_HOME:-$HOME/.local/state}/tmux-ai-attn/bell-hook.log"
mkdir -p "$(dirname "$log_file")" 2>/dev/null || true
if [ -f "$log_file" ]; then
  log_size=$(stat -c%s "$log_file" 2>/dev/null || stat -f%z "$log_file" 2>/dev/null || echo 0)
  if [ "$log_size" -gt 5242880 ] 2>/dev/null; then
    : > "$log_file" 2>/dev/null || true
  fi
fi
printf '%s pane=%s curr=%s start=%s path=%s\n' \
  "$(date -Is)" "$pane_id" "$pane_command" "$pane_start" "$pane_path" \
  >> "$log_file" 2>/dev/null || true

[ -z "$pane_id" ] && exit 0

# Match codex in either current or start command. Codex's CLI is a Node.js
# app, so pane_current_command is typically "node"; pane_start_command
# preserves the original "codex" invocation.
is_codex=0
for s in "$pane_command" "$pane_start"; do
  [ -z "$s" ] && continue
  first=${s%% *}
  base=${first##*/}
  case "$base" in
    codex|codex.*|codex-*|codex_*) is_codex=1; break ;;
  esac
done

if [ "$is_codex" -eq 1 ]; then
  "$ai_attn_cli" set-state \
    --agent codex \
    --state waiting \
    --pane-id "$pane_id" \
    --session-id "bell-$pane_id" \
    --cwd "${pane_path:-.}" \
    --reason terminal-bell \
    >/dev/null 2>&1 || true
fi
