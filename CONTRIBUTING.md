# Contributing to tmux-ai-attn

Thanks for your interest in contributing! This document covers the basics.

## Development Setup

1. Clone the repo and enable local builds:

```bash
git clone https://github.com/cosmicbuffalo/tmux-ai-attn ~/.tmux/plugins/tmux-ai-attn
```

2. Set dev build mode in your `tmux.conf`:

```tmux
set -g @ai_attn_dev_build "on"
```

3. Reload tmux to build from source on each plugin load:

```bash
tmux source-file ~/.config/tmux/tmux.conf
```

This requires Go 1.23+.

## Project Structure

```
tmux-ai-attn.tmux                  # Plugin entrypoint (sourced by TPM); loads tmux-ai-attn.conf
tmux-ai-attn.conf                  # Generated tmux configuration (do not edit)
tmux-ai-attn.rb                    # tmux.rb source of tmux-ai-attn.conf
lib/tmux_ai_attn.rb                # The plugin's tmux configuration, in tmux.rb
spec/compiled_config_spec.rb       # Fails while tmux-ai-attn.conf is stale
cmd/tmux-ai-attn-sync/
  main.go                          # Long-running sync daemon
  main_test.go                     # Tests
scripts/
  start-loop.sh                    # Launches the daemon
  ensure-binary.sh                 # Downloads or builds the binary
```

## Running Checks

A Makefile is provided for common tasks:

```bash
make build          # build the helper binary locally
make test           # run all tests
make format         # format Go source with gofmt
make lint           # run go vet
make conf           # compile lib/tmux_ai_attn.rb into tmux-ai-attn.conf
make spec           # check tmux-ai-attn.conf is up to date, and test the Ruby module
make check          # run the full pre-push check suite (gofmt, build, vet, test, specs, shellcheck) — same as CI
make install-hooks  # install a git pre-push hook that runs `make check` when pushing main (recommended)
make clean          # remove locally built binary
```

It's recommended to run `make install-hooks` after cloning so failing checks are caught locally before they reach CI. The hook only runs when the push includes `main`; feature-branch pushes are unaffected. Bypass any single push with `git push --no-verify` if needed.

CI runs the same checks on every push and PR.

## Making Changes

### Go code (`cmd/tmux-ai-attn-sync/`)

The sync daemon is a single `main` package with no external dependencies. Key functions:

- `run()` — Main loop: fsnotify watcher setup, adaptive tick timer, signal handling
- `syncOnce()` — Single sync cycle: query ai-attn, read tmux topology, compute state, batch-write options
- `tickForState()` — Determines display tick interval based on active states (fast for flash/spinner animation, 10s for active pane age text, 0 for idle); the event loop applies a 30s idle server-health check
- `buildFlashState()` — Core algorithm: maps ai-attn records to waiting/flash state per window and pane

When adding features, prefer keeping the subprocess count low. Batch tmux reads/writes where possible.

### tmux configuration (`lib/tmux_ai_attn.rb`)

The plugin's tmux configuration (option defaults, starting the sync loop, hooks, and the optional default formats) is written in Ruby with [tmux.rb](https://github.com/cosmicbuffalo/tmux.rb). Edit `lib/tmux_ai_attn.rb`, never `tmux-ai-attn.conf`, then regenerate and commit the compiled file:

```bash
bundle install   # once; needs Ruby 3.2+ and tmux. Not into vendor/: Go reads that as module vendoring
make conf
make spec
```

`make spec` (and CI) fails while `tmux-ai-attn.conf` differs from what tmux.rb compiles, and prints the diff. `scripts/test-tmux-conf.sh` loads the compiled file into a throwaway tmux server. The same module is what tmux.rb users include with `use TmuxAiAttn`, so keep its public methods (`config`, `to_config`, `window_status_format`, `window_status_current_format`) working.

### Shell scripts

All scripts use `set -euo pipefail`. Keep them POSIX-compatible where practical, but bash-specific features (arrays, `[[`) are acceptable since the shebang is `#!/usr/bin/env bash`.

### Tests

Add tests for any new logic in `buildFlashState` or `stateHash`. Tests use plain `testing` with no external frameworks.

## Commit Messages

- Use imperative mood ("Add feature", not "Added feature")
- First line under 72 characters
- Explain *why*, not just *what*

## Releases

Release binaries are built and published by CI. Merging a change to `main` that updates `VERSION` creates the matching tag automatically after tests pass, then cross-compiles `tmux-ai-attn-sync` for `linux-amd64`, `linux-arm64`, `darwin-amd64`, and `darwin-arm64`, generates `checksums.txt`, and creates a draft GitHub Release with the binaries attached.

Pushing a tag matching `v*` directly still triggers the same build and draft-release flow.

The bundled default helper version lives in the repo's `VERSION` file — bump it when preparing a `tmux-ai-attn` release so `scripts/ensure-binary.sh` downloads the matching `tmux-ai-attn` release. This is separate from the `ai-attn` CLI version configured by `@ai_attn_cli`.

## Submitting a PR

1. Fork the repo and create a feature branch
2. Make your changes and ensure all checks pass
3. Open a PR with a clear description of what changed and why
4. Link any related issues

## Reporting Issues

Open an issue on GitHub with:

- What you expected to happen
- What actually happened
- Your tmux version (`tmux -V`)
- Your OS and architecture
- Relevant log output (`LOG_DIR="${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/tmux-ai-attn}"; LOG_DIR="${LOG_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn/run}"; cat "$LOG_DIR"/tmux-ai-attn-*.log`)
