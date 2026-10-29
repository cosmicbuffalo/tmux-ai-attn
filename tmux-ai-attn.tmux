#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The plugin's tmux configuration lives in tmux-ai-attn.conf, compiled by
# tmux.rb from lib/tmux-ai-attn.rb (see CONTRIBUTING.md). tmux expands
# $TMUX_AI_ATTN_DIR while it loads the file, so the hooks hold this
# checkout's absolute path. Hidden (-h): new panes don't inherit it.
tmux set-environment -gh TMUX_AI_ATTN_DIR "$CURRENT_DIR" \; \
  source-file "$CURRENT_DIR/tmux-ai-attn.conf"
