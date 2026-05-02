# tmux-ai-attn

Tmux plugin that watches [`ai-attn`](https://github.com/cosmicbuffalo/ai-attn) state via filesystem notifications and sets tmux user options so you can style windows and panes that need your attention.

The plugin does not impose styles by default. It computes state and exposes flags so you can write your own `window-status-format`, `pane-border-format`, or status-bar snippets that react to AI attention signals.

It separates persistent waiting state from transient flash state, so you can build an "I saw it" UX without clearing the underlying attention signal.

## Supported Platforms

- Linux (amd64, arm64)
- macOS (amd64, arm64 / Apple Silicon)

## Requirements

- tmux 3.0 or later
- [`ai-attn`](https://github.com/cosmicbuffalo/ai-attn) installed and on `PATH` (or configured via `@ai_attn_cli`)
- `curl` or `wget` (for downloading the prebuilt helper binary)

Go is **not** required for normal use. The plugin automatically downloads a prebuilt helper binary for your platform.

If `ai-attn` is not installed when the plugin loads, it will set `@ai_attn_last_error` with installation instructions. Check with:

```bash
tmux show-option -gqv @ai_attn_last_error
```

## Install

### With TPM

Add to your `tmux.conf`:

```tmux
set -g @plugin 'cosmicbuffalo/tmux-ai-attn'
```

Then install with `prefix + I`.

### Manual

Clone the repo and source the entrypoint from your `tmux.conf`:

```bash
git clone https://github.com/cosmicbuffalo/tmux-ai-attn ~/.tmux/plugins/tmux-ai-attn
```

```tmux
run-shell "~/.tmux/plugins/tmux-ai-attn/tmux-ai-attn.tmux"
```

Reload your tmux config:

```bash
tmux source-file ~/.tmux.conf
# or, if you use the XDG config path:
tmux source-file ~/.config/tmux/tmux.conf
```

## Agent-Assisted Setup

If you use an AI coding agent (Claude Code, Codex, etc.), you can point it at the [AGENTS.md](AGENTS.md) file in this repo for guided installation and configuration. The agent will walk you through prerequisites, hook setup, styling preferences, and verification.

## Quick Start

### 1. Install ai-attn

Install [ai-attn](https://github.com/cosmicbuffalo/ai-attn):

```bash
curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/ai-attn/main/install.sh | bash
```

Or clone and install manually:

```bash
git clone https://github.com/cosmicbuffalo/ai-attn /tmp/ai-attn
cd /tmp/ai-attn
bash ./install.sh
```

Verify the installation:

```bash
ai-attn doctor
```

You should see that:

- `ai-attn` is on `PATH`
- state dir is `~/.local/state/ai-attn`
- config is `~/.config/ai-attn/config.toml` (or "default" if no config file is present)

### 2. Wire your AI CLI hooks

Set up hooks for whichever AI CLI tools you use. These hooks tell `ai-attn` when your AI agent needs attention.

The simplest path is to have an AI agent do it for you — point it at [ai-attn's AGENTS.md](https://github.com/cosmicbuffalo/ai-attn/blob/main/AGENTS.md) and it will edit the right config files for Claude Code, Codex, and OpenCode. For manual wiring instructions and the underlying hook payload format, see the same file.

### 3. Install tmux-ai-attn

Follow the [Install](#install) section above.

### 4. Configure the plugin

If `ai-attn` is not on your `PATH`, set the full path in your `tmux.conf`:

```tmux
set -g @ai_attn_cli "/home/YOU/.local/bin/ai-attn"
```

All other options have sensible defaults. See [Configuration](#configuration) for the full list.

### 5. Make attention visible

Add styling to your `tmux.conf` that consumes the plugin-set flags. See [Styling Examples](#styling-examples) below for window tabs, pane borders, and status bar badges.

### 6. Reload tmux

```bash
tmux source-file ~/.tmux.conf
# or, if you use the XDG config path:
tmux source-file ~/.config/tmux/tmux.conf
```

### 7. Test it

Use the built-in test command to verify everything is working. It fires a test signal, pauses so you can observe your tmux styling react, then auto-clears:

```bash
ai-attn test
```

You should see your window tab / pane border / status bar change as the test signal progresses through its stages.

## Configuration

All options are set as tmux global options. Values shown are the defaults.

```tmux
# Path or command name for the ai-attn CLI.
set -g @ai_attn_cli "ai-attn"

# Seconds a new attention signal flashes in the currently viewed window
# before being auto-acknowledged.
set -g @ai_attn_seen_flash_seconds "3"

# Force a tmux client redraw after each sync cycle that changes state.
set -g @ai_attn_refresh_client "on"
```

### Upgrading

The helper binary version is pinned by the `VERSION` file in this repo. To upgrade the helper, update the plugin via TPM (`prefix + U`) — TPM pulls the new commit, the new `VERSION` is read, and `ensure-binary.sh` downloads the matching binary on the next tmux reload.

## Tmux Options Set by the Plugin

The plugin writes these tmux user options on each sync cycle. Use them in your format strings to build custom styling.

### Global options

| Option | Type | Description |
|--------|------|-------------|
| `@ai_attn_any_waiting` | `0` / `1` | Whether any pane in the session is waiting for attention. |
| `@ai_attn_waiting_panes` | integer | Number of panes currently waiting. |
| `@ai_attn_waiting_windows` | integer | Number of windows containing at least one waiting pane. |
| `@ai_attn_flash_phase` | `0` / `1` | Alternates every second while any pane is waiting. Use in format strings to create a flashing effect. |
| `@ai_attn_last_error` | string | Empty on success. Contains the error message if `ai-attn` or tmux communication failed on the last sync. |
| `@ai_attn_state_hash` | string | Internal. Used to skip redundant updates when state has not changed. |

### Per-window options

| Option | Type | Description |
|--------|------|-------------|
| `@ai_attn_window_waiting` | `0` / `1` | Whether this window contains any waiting panes. |
| `@ai_attn_window_waiting_count` | integer | Number of waiting panes in this window. |
| `@ai_attn_window_flash` | `0` / `1` | Transient flag. Set to `1` when a new attention signal arrives and the window has not yet been acknowledged. Stays `1` for background windows until the user switches to them. For the active window, stays `1` for `@ai_attn_seen_flash_seconds` then auto-clears. |

### Per-pane options

| Option | Type | Description |
|--------|------|-------------|
| `@ai_attn_pane_state` | string | Current state of the pane (`working`, `waiting`, `done`, or empty when idle). |
| `@ai_attn_pane_agent` | string | Name of the AI agent that raised the signal (e.g., `codex`, `claude`). |
| `@ai_attn_pane_reason` | string | Reason for the current state (e.g., `permission_request`, `elicitation`, `agent-turn-complete`). |
| `@ai_attn_pane_updated_at` | integer | Unix timestamp of the last state change. |
| `@ai_attn_pane_flash` | `0` / `1` | Transient flag. `1` while the signal is new and within the flash grace period. |

### State vs. Flash

- **State** (`@ai_attn_pane_state`) and **waiting** (`@ai_attn_window_waiting`) are persistent. They reflect the current attention state reported by `ai-attn`, regardless of whether you've seen it.
- **Flash** (`@ai_attn_window_flash`, `@ai_attn_pane_flash`) is transient. It represents "new, unseen" attention. For background windows, flash stays on until you switch to that window. For the active window, flash auto-clears after `@ai_attn_seen_flash_seconds`.

Use state/waiting flags for steady-state indicators (e.g., a colored window tab). Use flash flags for urgent, temporary alerts (e.g., a blinking border).

## Styling Examples

### Window tab styling

Use `@ai_attn_window_flash` with `@ai_attn_flash_phase` for a flashing effect on unseen attention, and `@ai_attn_window_waiting` for a steady indicator.

```tmux
set -g status-interval 1

# Flashing for unseen signals, steady color for acknowledged but still waiting:
set -g window-status-format '\
#{?@ai_attn_window_flash,\
#{?@ai_attn_flash_phase,#[fg=colour231,bg=colour160,bold],#[fg=colour231,bg=colour52,bold]},\
#{?@ai_attn_window_waiting,#[fg=colour231,bg=colour52],#[fg=colour244]}}\
 #I:#W #[default]'

set -g window-status-current-format '\
#{?@ai_attn_window_flash,\
#{?@ai_attn_flash_phase,#[fg=colour16,bg=colour226,bold],#[fg=colour16,bg=colour208,bold]},\
#{?@ai_attn_window_waiting,#[fg=colour16,bg=colour208],#[fg=colour255,bg=colour24,bold]}}\
 #I:#W #[default]'
```

### Pane border styling

```tmux
set -g pane-border-status top
set -g pane-border-format '\
#{?@ai_attn_pane_state,\
#[fg=black,bg=yellow,bold] #{@ai_attn_pane_state} #{@ai_attn_pane_agent} #[default],\
}#{pane_index} #{pane_title}'
```

### Status bar badge

```tmux
set -g status-right '\
#{?@ai_attn_any_waiting,#[fg=black,bg=yellow] ATTN:#{@ai_attn_waiting_panes} ,}\
%H:%M'
```

## How It Works

1. On plugin load (`tmux-ai-attn.tmux`), default options are set and a long-running helper binary (`tmux-ai-attn-sync`) is launched in the background.
2. The binary watches the ai-attn state directory for changes via filesystem notifications (inotify on Linux, kqueue on macOS). When a state file changes, it refreshes from `ai-attn list --json` (with a 5-second timeout) and caches the resulting records.
3. It maps `ai-attn` records to tmux panes and windows, computes waiting/flash state, and writes all tmux user options in batched tmux calls for efficiency.
4. If the visual state has not changed since the last sync, no tmux writes are performed.
5. Display-only ticks reuse the cached records, so age text and flash animation updates do not need to respawn `ai-attn` on every timer tick.
6. The binary self-terminates when the tmux server exits (all sessions closed) and cleans up its PID file.

### Signal path

```
AI CLI hook  -->  ai-attn state dir  -->  ai-attn list --json
                                              |
                                              v
                                     tmux-ai-attn-sync
                                              |
                                              v
                                     tmux user options
                                              |
                                              v
                                  your format strings render it
```

The plugin uses the `pane_id` field from `ai-attn` records to determine which tmux pane to style.

### Codex permission prompts (terminal bell)

Codex's `notify` channel only emits `agent-turn-complete` in current versions, so mid-turn permission prompts never reach `ai-attn` through the normal hook path. Codex does emit an ASCII BEL on permission prompts, which tmux surfaces via the `alert-bell` hook.

The plugin registers an `alert-bell` hook that filters to codex panes and writes a `waiting` ai-attn record when one rings. No configuration is required — other agents are unaffected because they have their own hook integrations that report waiting state directly. Bell events are logged to `${XDG_STATE_HOME:-~/.local/state}/tmux-ai-attn/bell-hook.log`.

## Debugging

### Check ai-attn state

```bash
ai-attn list
ai-attn list --json
```

### Check tmux options

```bash
# Global options
tmux show-options -g | grep @ai_attn

# Window options for the current window
tmux show-window-options | grep @ai_attn

# Pane options for the current pane
tmux show-options -p | grep @ai_attn
```

### Check the background process

```bash
# Is the sync daemon running?
ps aux | grep tmux-ai-attn

# View the sync daemon log
LOG_DIR="${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/tmux-ai-attn}"
LOG_DIR="${LOG_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn/run}"
ls "$LOG_DIR"/tmux-ai-attn-*.log
cat "$LOG_DIR"/tmux-ai-attn-*.log
```

### Check for errors

```bash
tmux show-option -gqv @ai_attn_last_error
```

### Simulate an attention signal

```bash
# Built-in test: fires a signal, cycles through reasons, then auto-clears
ai-attn test

# Or manually:
ai-attn set-state --agent test --session-id test --cwd "$PWD" --reason permission_required
ai-attn list
# window tab and/or pane border should react

ai-attn clear
```

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, including local build mode, the Makefile targets for tests and checks, and how releases are produced.

## License

[MIT](LICENSE)
