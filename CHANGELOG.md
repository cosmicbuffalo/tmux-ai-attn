# Changelog

## 0.2.0 — 2026-05-15

- Spinner animation for working panes — cycles through configurable frames at the tick rate, giving visual feedback that the agent is actively processing
- Pre-rendered `@ai_attn_window_segment` option containing icon, color, and flash state for easy status-line integration via `#{E:@ai_attn_window_segment}`
- Window state aggregation via `@ai_attn_window_state` — rolls up per-pane states with priority: waiting > working > stopped > done
- Configurable icons (`@ai_attn_icon_waiting`, `@ai_attn_icon_stopped`, `@ai_attn_icon_done`), colors (`@ai_attn_color_*`), spinner frames (`@ai_attn_spinner_frames`), tick rate (`@ai_attn_tick_ms`), and flash multiplier (`@ai_attn_flash_multiplier`)
- Faster flash animation (480ms cadence, up from 1s) using counter-based phase toggling instead of wall-clock derivation
- Reload resilience: `tmux source-file` no longer accumulates duplicate hooks — old plugin hooks are cleared before re-registering
- Daemon restart on plugin reload via `--restart` flag, so config changes take effect immediately
- Spinner frame advancement now follows `@ai_attn_tick_ms` so custom tick rates stay in step with the animation
- When `ai-attn` is missing, `@ai_attn_last_error` now points at the pinned `ai-attn` `v0.2.0` installer URL
- Fixed flock file descriptor leak to child daemon process (`9>&-`)
- Fixed `set_default_option` not re-applying defaults after daemon shutdown cleared options to empty strings
- Requires tmux 3.5 or newer

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
