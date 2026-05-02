# AGENTS.md — Agent-Guided Setup for tmux-ai-attn

This file is intended to be read by an AI coding agent (Claude Code, Codex, etc.) to guide a user through installing, configuring, and testing `tmux-ai-attn`.

## What This Project Does

`tmux-ai-attn` is a tmux plugin that visually highlights windows and panes where an AI CLI agent (Claude Code, Codex, etc.) is waiting for your attention. It watches the [`ai-attn`](https://github.com/cosmicbuffalo/ai-attn) state directory for changes via filesystem notifications (inotify/kqueue) and writes state into tmux user options, which the user's format strings can consume.

## Setup Instructions

Follow these steps in order. At several points you will need to **ask the user** for their preferences — do not assume defaults where the instructions say to prompt.

---

### Step 1: Verify Prerequisites

Check each of the following and report the results to the user:

1. **tmux version**: Run `tmux -V`. Version must be 3.0 or later.
2. **tmux is running**: The user must be inside an active tmux session. Verify with `echo $TMUX` (should be non-empty).
3. **Platform**: Run `uname -s` and `uname -m`. Supported: Linux or macOS, on amd64 (x86_64) or arm64 (aarch64/arm64).
4. **curl or wget**: At least one must be available. Check with `command -v curl` and `command -v wget`.
5. **TPM (Tmux Plugin Manager)**: Check if TPM is installed by looking for `~/.tmux/plugins/tpm/tpm`. If not installed, inform the user they need to install it first: `git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm` and ensure their `tmux.conf` contains `run '~/.tmux/plugins/tpm/tpm.tmux'` at the bottom.

If any prerequisites fail, stop and help the user resolve them before continuing.

---

### Step 2: Install ai-attn

Check if `ai-attn` is already installed:

```bash
command -v ai-attn && ai-attn doctor
```

If `ai-attn` is not found or `doctor` reports issues, install it. The fastest method is the one-liner:

```bash
curl -fsSL https://raw.githubusercontent.com/cosmicbuffalo/ai-attn/main/install.sh | bash
```

Alternatively, clone and install:

```bash
git clone https://github.com/cosmicbuffalo/ai-attn /tmp/ai-attn
cd /tmp/ai-attn
bash ./install.sh
```

After installation, verify:

```bash
ai-attn doctor
```

Expected output should show:
- `ai-attn` is on `PATH`
- state dir is `~/.local/state/ai-attn`
- config is `~/.config/ai-attn/config.toml` (or `default` if no config file is present)

If `ai-attn` is installed but not on `PATH`, note the full path — it will be needed for the `@ai_attn_cli` tmux option in Step 5.

---

### Step 3: Configure AI CLI Hooks

Hook wiring lives in the ai-attn project, not here. Read [ai-attn's AGENTS.md](https://github.com/cosmicbuffalo/ai-attn/blob/main/AGENTS.md) and follow the per-agent instructions for whichever CLIs the user actually uses (Claude Code, Codex, OpenCode). That file knows how to edit `~/.claude/settings.json`, `~/.codex/config.toml`, and `~/.config/opencode/opencode.jsonc` correctly.

**Codex note:** Codex's `notify` channel only emits `agent-turn-complete`, so mid-turn permission prompts won't reach ai-attn through the normal hook path. `tmux-ai-attn` handles this automatically by registering a tmux `alert-bell` hook (codex emits a terminal bell on permission prompts) — no extra configuration is needed beyond installing the plugin in Step 4.

When you're done, return here for Step 4.

---

### Step 4: Install tmux-ai-attn Plugin

First, find the user's `tmux.conf`. Check these locations in order:
- `~/.config/tmux/tmux.conf`
- `~/.tmux.conf`

If neither exists, ask the user where their tmux configuration lives.

Add the TPM plugin line to the user's `tmux.conf` (if not already present):

```tmux
set -g @plugin 'cosmicbuffalo/tmux-ai-attn'
```

This line should go **before** the `run '~/.tmux/plugins/tpm/tpm'` line at the bottom of the file.

After adding the plugin line, install it by running:

```bash
# Reload tmux config
tmux source-file <path-to-tmux.conf>
```

Then tell the user to press `prefix + I` (capital I) in tmux to trigger TPM's plugin install. Wait for the user to confirm the install completed.

Alternatively, if the user prefers to install without the interactive key binding:

```bash
~/.tmux/plugins/tpm/bin/install_plugins
```

Verify the plugin was installed:

```bash
ls ~/.tmux/plugins/tmux-ai-attn/tmux-ai-attn.tmux
```

---

### Step 5: Configure the Plugin

If `ai-attn` is not on `PATH` (detected in Step 2), add to `tmux.conf`:

```tmux
set -g @ai_attn_cli "/full/path/to/ai-attn"
```

**Ask the user:** "How long should new attention signals flash before being auto-acknowledged in the active window? (default: 3 seconds)"

If the user wants a different value, add to `tmux.conf`:

```tmux
set -g @ai_attn_seen_flash_seconds "<seconds>"
```

---

### Step 6: Configure Styling

This is the most preference-dependent step. The plugin exposes tmux user options but does not impose any visual styles by default. The user must choose how they want attention signals to appear.

**Ask the user:** "How would you like AI attention signals to appear in tmux? Choose one or more:"

1. **Window tab highlighting** — Color/flash the window tab in the status bar when a window has a pane waiting for attention
2. **Pane border annotation** — Show agent name and reason in the pane border
3. **Status bar badge** — Show an attention count badge in the status bar

#### Option: Window tab highlighting

Ensure `status-interval` is set to 1 for timely updates:

```tmux
set -g status-interval 1
```

**Ask the user:** "Do you want unseen attention signals to flash/blink, or just show a steady color?"

**With flashing** (alternates colors every second for unseen signals, steady color for acknowledged-but-still-waiting):

```tmux
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

**Without flashing** (steady color only):

```tmux
set -g window-status-format '\
#{?@ai_attn_window_waiting,#[fg=colour231,bg=colour52],#[fg=colour244]}\
 #I:#W #[default]'

set -g window-status-current-format '\
#{?@ai_attn_window_waiting,#[fg=colour16,bg=colour208],#[fg=colour255,bg=colour24,bold]}\
 #I:#W #[default]'
```

**Important:** If the user already has custom `window-status-format` or `window-status-current-format` strings in their config, you should integrate the `@ai_attn_*` conditionals into their existing format strings rather than overwriting them. Read their current format strings first and merge the attention styling in.

#### Option: Pane border annotation

```tmux
set -g pane-border-status top
set -g pane-border-format '\
#{?#{==:#{@ai_attn_pane_state},waiting},\
#[fg=black,bg=yellow,bold] AI WAIT #{@ai_attn_pane_agent} #{@ai_attn_pane_reason} #[default],\
}#{pane_index} #{pane_title}'
```

**Note:** If the user already has a custom `pane-border-format`, integrate the attention annotation into their existing format rather than replacing it.

#### Option: Status bar badge

Add to the user's `status-right` (or `status-left`):

```tmux
set -g status-right '\
#{?@ai_attn_any_waiting,#[fg=black,bg=yellow] ATTN:#{@ai_attn_waiting_panes} ,}\
%H:%M'
```

**Note:** If the user already has a custom `status-right`, prepend or append the badge to their existing string rather than replacing it.

---

### Step 7: Reload and Test

Reload the tmux config:

```bash
tmux source-file <path-to-tmux.conf>
```

Verify the plugin daemon is running:

```bash
ps aux | grep tmux-ai-attn-sync
```

Check for errors:

```bash
tmux show-option -gqv @ai_attn_last_error
```

Run the built-in test command, which fires a test signal, cycles through different reasons with pauses, then auto-clears:

```bash
ai-attn test
```

Ask the user: "The test command cycles through attention states over several seconds. Did you see visual changes in your tmux status bar, window tabs, or pane borders? Describe what you saw."

If the styling is working, verify the tmux options are being set:

```bash
tmux show-options -g | grep @ai_attn
tmux show-window-options | grep @ai_attn
tmux show-options -p | grep @ai_attn
```

If nothing appeared, debug:

1. Check `ai-attn list` — does it show a record?
2. Check `tmux show-option -gqv @ai_attn_any_waiting` — is it `1`?
3. Check the daemon log:

   ```bash
   LOG_DIR="${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/tmux-ai-attn}"
   LOG_DIR="${LOG_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn/run}"
   cat "$LOG_DIR"/tmux-ai-attn-*.log
   ```
4. Check `tmux show-option -gqv @ai_attn_last_error` for error messages.

---

### Step 8: Final Verification

Run through this checklist and report results to the user:

- [ ] `ai-attn doctor` passes
- [ ] `tmux-ai-attn-sync` process is running (`ps aux | grep tmux-ai-attn-sync`)
- [ ] `@ai_attn_last_error` is empty
- [ ] `ai-attn test` triggers visual changes in tmux and auto-clears
- [ ] AI CLI hook is registered (Claude Code settings or Codex config.toml)

If all checks pass, the setup is complete. Inform the user that the next time their AI agent pauses for permission or completes a turn, their tmux session will visually indicate which window/pane needs attention.

---

## Reference: All Configuration Options

These tmux options can be set in `tmux.conf` with `set -g`:

| Option | Default | Description |
|--------|---------|-------------|
| `@ai_attn_cli` | `"ai-attn"` | Path or name of the ai-attn CLI |
| `@ai_attn_seen_flash_seconds` | `"3"` | Flash duration for active window before auto-acknowledge |
| `@ai_attn_refresh_client` | `"on"` | Force tmux redraw after state changes |

The helper binary version is pinned by the `VERSION` file in the repo and tracked by TPM updates. There's no tmux option to override it.

## Reference: Tmux Options Set by the Plugin

See the [README](README.md#tmux-options-set-by-the-plugin) for the full list of global, per-window, and per-pane options the plugin writes, including the distinction between "waiting" (persistent) and "flash" (transient/unseen) state.
