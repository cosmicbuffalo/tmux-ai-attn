# Changelog

## 0.3.1 — 2026-07-10

- Replace the Linux-only `flock` launcher lock with a portable lock that works on stock macOS.
- Hash tmux socket paths for runtime filenames, safely migrate the legacy key, and verify PID ownership before restart signals.
- Keep an idle daemon health timer so it exits and removes its PID file after the owning tmux server disappears.
- Require `ai-attn` v0.3.2 or newer so pane records always use socket-scoped session keys.
- Update the missing-`ai-attn` install hint to the `ai-attn` `v0.3.2` installer.
- Document Codex `PreToolUse` and `PostToolUse` lifecycle hooks now reported by `ai-attn v0.3.1`; no daemon change is required because `tmux-ai-attn` already handles `working` records generically.

## 0.3.0 — 2026-07-07

- Scope state records to their owning tmux server. A record carrying a `socket` field is ignored unless it matches this daemon's `--tmux-socket`. Fixes cross-server window flashing: a `pane_id` written by one tmux server was being matched against an unrelated pane with the same `%N` in another tmux server, lighting up the wrong window. Records without a `socket` (older `ai-attn`) still match any server, preserving prior behavior.
- Stamp terminal-bell records created by the plugin's Codex fallback hook with the current tmux server socket when supported by `ai-attn`, so bell-generated records get the same cross-server scoping.
- Create the matching `tmux-ai-attn` release tag automatically when a `VERSION` bump is merged to `main`, then build the release assets and draft the GitHub Release in CI.
- Remove the unused `@ai_attn_version` tmux option.

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
