# Changelog

## 0.1.0 — 2026-05-02

- Long-running Go daemon (`tmux-ai-attn-sync`) watches ai-attn state files via filesystem notifications and synchronizes attention state to tmux user options
- Window-level and pane-level attention indicators via `@ai_attn_window_waiting` and `@ai_attn_pane_state`
- Flash/seen behavior: unseen attention signals flash until acknowledged, then settle to steady state
- Adaptive display ticking (1s for flash animation, 10s for active pane age text, idle when no state is active)
- Configurable flash grace period and format strings
- Prebuilt binaries for Linux and macOS (amd64 and arm64) with automatic download via TPM
- Automatic daemon lifecycle management tied to tmux hooks
- Fallback to source build when prebuilt binary is unavailable
- `alert-bell` → ai-attn bridge for Codex panes, so permission prompts that Codex only signals via terminal bell still flash the status line
