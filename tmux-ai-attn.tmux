#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION_FILE="$CURRENT_DIR/VERSION"
DEFAULT_VERSION="$(cat "$VERSION_FILE" 2>/dev/null || true)"

# Read all current @ai_attn_ options in one call.
existing="$(tmux show-options -gq 2>/dev/null || true)"

set_default_option() {
  local name="$1"
  local value="$2"
  # Re-apply the default when the option is absent entirely, or when it has
  # been reset to the explicit '' placeholder left by clearAllAttnOptions on
  # daemon shutdown. Any other value (including values tmux renders quoted
  # due to whitespace or multibyte characters) is treated as user-configured
  # and preserved.
  if ! printf '%s\n' "$existing" | grep -q "^${name} " \
     || printf '%s\n' "$existing" | grep -qE "^${name} ''\$"; then
    DEFAULT_BATCH+=("set-option" "-gq" "$name" "$value" ";")
  fi
}

DEFAULT_BATCH=()
set_default_option @ai_attn_cli "ai-attn"
set_default_option @ai_attn_version "$DEFAULT_VERSION"
set_default_option @ai_attn_dev_build "off"
set_default_option @ai_attn_auto_install "off"
set_default_option @ai_attn_seen_flash_seconds "3"
set_default_option @ai_attn_enable_default_formats "off"
set_default_option @ai_attn_refresh_client "on"
set_default_option @ai_attn_icon_waiting "⚠"
set_default_option @ai_attn_icon_stopped "⏸"
set_default_option @ai_attn_icon_done "✓"
set_default_option @ai_attn_color_waiting "yellow"
set_default_option @ai_attn_color_stopped "colour196"
set_default_option @ai_attn_color_working "#d88786"
set_default_option @ai_attn_color_done "green"
set_default_option @ai_attn_color_flash_bg "colour226"
set_default_option @ai_attn_color_flash_fg "colour16"
set_default_option @ai_attn_color_text_fg "colour255"
set_default_option @ai_attn_spinner_frames "·,·,✢,✳,✶,✽,✻,✻,✻,✽,✶,✳,✢,·"
set_default_option @ai_attn_tick_ms "120"
set_default_option @ai_attn_flash_multiplier "4"

if [ ${#DEFAULT_BATCH[@]} -gt 0 ]; then
  # Remove trailing ";"
  unset 'DEFAULT_BATCH[${#DEFAULT_BATCH[@]}-1]'
  tmux "${DEFAULT_BATCH[@]}"
fi

"$CURRENT_DIR/scripts/start-loop.sh" "$CURRENT_DIR" "--restart" || true

# Remove any previously registered hooks from this plugin before re-registering.
# This prevents hook accumulation across repeated source-file reloads.
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

ensure_hook_contains() {
  local hook="$1"
  local command="$2"
  local existing_hook
  existing_hook="$(tmux show-hooks -g "$hook" 2>/dev/null || true)"
  if ! printf '%s\n' "$existing_hook" | grep -Fq "$command"; then
    tmux set-hook -ag "$hook" "$command"
  fi
}

# Hooks must not hard-fail `tmux new-session` (or any other triggering
# command) if the plugin gets moved or deleted after registration. Two
# guarantees are baked in at the tmux layer here:
#   1. `run-shell -b` runs the hook body in the background, so exit status
#      never propagates back to the command that fired the hook.
#   2. The shell body checks the script path at fire time. If it's missing
#      (plugin moved/deleted) or start-loop.sh errors, the failure is
#      logged to HOOK_LOG and a transient tmux display-message is shown,
#      so it's noticed but never blocks.
HOOK_LOG="${TMUX_AI_ATTN_LOG:-${XDG_STATE_HOME:-$HOME/.local/state}/tmux-ai-attn/hooks.log}"
mkdir -p "$(dirname "$HOOK_LOG")" 2>/dev/null || true

START_LOOP="$CURRENT_DIR/scripts/start-loop.sh"
# Paths are substituted once, at registration time, and then literal inside
# the single-quoted sh command tmux will run via run-shell -b. If any of
# these paths could contain single quotes we would need extra escaping —
# tmux-ai-attn's own install layout never does.
#
# The $(date ...) stays in single quotes on purpose — we want it evaluated
# at hook-fire time, not at registration time, so log timestamps reflect
# when a failure actually happened. shellcheck can't tell, hence SC2016.
# shellcheck disable=SC2016
HOOK_SH='if [ -x "'"$START_LOOP"'" ]; then "'"$START_LOOP"'" "'"$CURRENT_DIR"'" >> "'"$HOOK_LOG"'" 2>&1 || { printf "%s [hook] start-loop.sh failed\n" "$(date +%Y-%m-%dT%H:%M:%S)" >> "'"$HOOK_LOG"'" 2>/dev/null; tmux display-message "[tmux-ai-attn] hook failed (see '"$HOOK_LOG"')"; }; else printf "%s [hook] plugin missing: %s\n" "$(date +%Y-%m-%dT%H:%M:%S)" "'"$START_LOOP"'" >> "'"$HOOK_LOG"'" 2>/dev/null; tmux display-message "[tmux-ai-attn] plugin missing (see '"$HOOK_LOG"')"; fi'
HOOK_COMMAND="run-shell -b '$HOOK_SH'"

for hook in client-attached client-session-changed session-created after-new-session; do
  clear_plugin_hooks "$hook"
  ensure_hook_contains "$hook" "$HOOK_COMMAND"
done

# alert-bell → ai-attn waiting record (codex only).
#
# Codex's notify channel only emits agent-turn-complete in current versions,
# so permission prompts mid-turn never reach ai-attn through the hook path.
# Codex emits a terminal bell on permission prompts; routing alert-bell
# through bell-to-ai-attn.sh lets us write a `waiting` record so the status
# line flashes. The script filters to codex panes — other agents have their
# own hook integrations that report waiting state directly.
BELL_SCRIPT="$CURRENT_DIR/scripts/bell-to-ai-attn.sh"
# shellcheck disable=SC2016
BELL_HOOK_SH='[ -x "'"$BELL_SCRIPT"'" ] && "'"$BELL_SCRIPT"'" "#{pane_current_command}" "#{pane_start_command}" "#{pane_id}" "#{pane_current_path}" "#{@ai_attn_cli}"'
BELL_HOOK_COMMAND="run-shell -b '$BELL_HOOK_SH'"
clear_plugin_hooks "alert-bell"
ensure_hook_contains "alert-bell" "$BELL_HOOK_COMMAND"

if [ "$(tmux show-option -gqv @ai_attn_enable_default_formats)" = "on" ]; then
  tmux set -g status-interval 1
  tmux set -g window-status-format '#{?@ai_attn_window_waiting,#{?@ai_attn_window_flash,#{?@ai_attn_flash_phase,#[fg=colour231,bg=colour160,bold],#[fg=colour231,bg=colour52,bold]},#[fg=colour231,bg=colour52]},#[fg=colour244]} #I:#W #[default]'
  tmux set -g window-status-current-format '#{?@ai_attn_window_waiting,#{?@ai_attn_window_flash,#{?@ai_attn_flash_phase,#[fg=colour16,bg=colour226,bold],#[fg=colour16,bg=colour208,bold]},#[fg=colour16,bg=colour208]},#[fg=colour255,bg=colour24,bold]} #I:#W #[default]'
fi
