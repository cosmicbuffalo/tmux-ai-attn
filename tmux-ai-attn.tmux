#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The plugin's tmux configuration lives in tmux-ai-attn.conf, compiled by
# tmux.rb from lib/tmux-ai-attn.rb (see CONTRIBUTING.md). It sets option
# defaults, starts the sync loop, and registers its hooks in a fixed array
# slot, so loading it again replaces them rather than adding copies.

# Remove hooks earlier releases appended outside that slot. Keep this for one
# release cycle so reloading cleans up upgrades without restarting tmux; it
# also removes the retired alert-bell hook, without touching unrelated hooks:
# a terminal bell is not a lifecycle event and can occur after Codex's Stop
# hook has already reported `done`, producing a false `waiting` state.
clear_plugin_hooks() {
  local hook="$1"
  local raw
  raw="$(tmux show-hooks -g 2>/dev/null || true)"
  # If no hooks reference our plugin dir, nothing to do.
  if ! printf '%s\n' "$raw" | grep -Fq "$CURRENT_DIR"; then
    return
  fi
  # Collect lines for this hook event, keeping non-plugin ones.
  # tmux 3.5+ always emits hooks in the array form "hook[N] cmd", but older
  # tmux versions emit a single non-appending hook as "hook cmd" (no [N]).
  # Match both forms so a user-defined single hook isn't silently dropped
  # by the subsequent `set-hook -gu`.
  local others=()
  local cmd
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    case "$line" in
      "${hook}["*) cmd="${line#*] }" ;;
      "${hook} "*) cmd="${line#"${hook}" }" ;;
      *) continue ;;
    esac
    if printf '%s' "$line" | grep -Fq "$CURRENT_DIR"; then
      continue
    fi
    others+=("$cmd")
  done <<< "$raw"
  # Unset and re-add non-plugin hooks.
  tmux set-hook -gu "$hook" 2>/dev/null || true
  for cmd in "${others[@]+"${others[@]}"}"; do
    tmux set-hook -ag "$hook" "$cmd"
  done
}

for hook in client-attached client-session-changed session-created after-new-session alert-bell; do
  clear_plugin_hooks "$hook"
done

# tmux expands $TMUX_AI_ATTN_DIR while it loads the file, so the hooks hold
# this checkout's absolute path. Hidden (-h): new panes don't inherit it.
tmux set-environment -gh TMUX_AI_ATTN_DIR "$CURRENT_DIR" \; \
  source-file "$CURRENT_DIR/tmux-ai-attn.conf"
