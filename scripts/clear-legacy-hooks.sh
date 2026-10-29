#!/usr/bin/env bash
# Remove the hooks earlier releases of this plugin appended to tmux's hook
# arrays. The plugin now owns one fixed slot per event (see HOOK_INDEX in
# lib/tmux-ai-attn.rb), which loading it again simply replaces, so this only
# unsets other slots whose command runs a script from this plugin directory.
# Other hooks keep their slots. Also removes the retired alert-bell hook: a
# terminal bell is not a lifecycle event and can occur after Codex's Stop hook
# has already reported `done`, producing a false `waiting` state.
#
# Keep this for one release cycle after v0.3.5, then delete it and the
# run-shell that calls it.
set -euo pipefail

PLUGIN_DIR="${1:?plugin dir required}"
HOOK_INDEX="${2:?hook index required}"

tmux show-hooks -g 2>/dev/null | while IFS= read -r line; do
  # tmux 3.5+ prints "event[N] command"; older versions print a single,
  # non-appended hook as "event command".
  slot="${line%% *}"
  case "${slot%%\[*}" in
    client-attached|client-session-changed|session-created|after-new-session|alert-bell) ;;
    *) continue ;;
  esac
  case "$slot" in
    *"[$HOOK_INDEX]") continue ;;
  esac
  case "$line" in
    *"$PLUGIN_DIR/scripts/"*) tmux set-hook -gu "$slot" ;;
  esac
done
