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
tmux-ai-attn.tmux                  # Plugin entrypoint (sourced by TPM)
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
make check          # run the full pre-push check suite (gofmt, build, vet, test, shellcheck) — same as CI
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
- `tickForState()` — Determines display tick interval based on active states (1s for flash animation, 10s for active pane age text, 0 for idle)
- `buildFlashState()` — Core algorithm: maps ai-attn records to waiting/flash state per window and pane

When adding features, prefer keeping the subprocess count low. Batch tmux reads/writes where possible.

### Shell scripts

All scripts use `set -euo pipefail`. Keep them POSIX-compatible where practical, but bash-specific features (arrays, `[[`) are acceptable since the shebang is `#!/usr/bin/env bash`.

### Tests

Add tests for any new logic in `buildFlashState` or `stateHash`. Tests use plain `testing` with no external frameworks.

## Commit Messages

- Use imperative mood ("Add feature", not "Added feature")
- First line under 72 characters
- Explain *why*, not just *what*

## Releases

Release binaries are built and published by CI. Pushing a tag matching `v*` triggers the `build-release` and `release` jobs in `.github/workflows/ci.yml`, which cross-compile `tmux-ai-attn-sync` for `linux-amd64`, `linux-arm64`, `darwin-amd64`, and `darwin-arm64`, generate `checksums.txt`, and create a draft GitHub Release with the binaries attached.

The bundled default helper version lives in the repo's `VERSION` file — bump it before tagging so `scripts/ensure-binary.sh` downloads the matching release.

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
