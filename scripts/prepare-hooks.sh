#!/usr/bin/env bash
# Prepare tmux's hook arrays before the plugin registers its hooks, which it
# keeps in one fixed slot per event (HOOK_INDEX in lib/tmux-ai-attn.rb) so
# that loading it again replaces them rather than adding copies.
set -euo pipefail

PLUGIN_DIR="${1:?plugin dir required}"
HOOK_INDEX="${2:?hook index required}"
LOG="${TMUX_AI_ATTN_LOG:-${XDG_STATE_HOME:-$HOME/.local/state}/tmux-ai-attn/hooks.log}"

log() {
  mkdir -p "${LOG%/*}" 2>/dev/null || true
  printf '%s [setup] %s\n' "$(date +%Y-%m-%dT%H:%M:%S)" "$1" >> "$LOG" 2>/dev/null || true
}

tmux show-hooks -g 2>/dev/null | while IFS= read -r line; do
  # tmux 3.5+ prints "event[N] command"; older versions print a single,
  # non-appended hook as "event command".
  slot="${line%% *}"
  event="${slot%%\[*}"
  command="${line#* }"
  case "$event" in
    client-attached|client-session-changed|session-created|after-new-session|alert-bell) ;;
    *) continue ;;
  esac

  if [ "$slot" = "${event}[$HOOK_INDEX]" ]; then
    # Something else holds the plugin's slot: the user's own hook, or another
    # copy of the plugin. Move it to the end of the array instead of letting
    # the plugin overwrite it.
    case "$line" in
      *"$PLUGIN_DIR/scripts/start-loop.sh"*) ;;
      *)
        tmux set-hook -ag "$event" "$command"
        log "moved a hook out of $slot, which tmux-ai-attn uses: $command"
        tmux display-message "[tmux-ai-attn] moved your $slot hook to the end of its array (see $LOG)" 2>/dev/null || true
        ;;
    esac
    continue
  fi

  # Remove the hooks earlier releases of this plugin appended in other slots,
  # one slot at a time, so other hooks keep theirs. This includes the retired
  # alert-bell hook: a terminal bell is not a lifecycle event and can occur
  # after Codex's Stop hook has already reported `done`, producing a false
  # `waiting` state. Keep this part for one release cycle after v0.3.5.
  case "$line" in
    *"$PLUGIN_DIR/scripts/"*) tmux set-hook -gu "$slot" ;;
  esac
done
