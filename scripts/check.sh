#!/usr/bin/env bash
# Run the full local check suite (mirrors .github/workflows/ci.yml).
# Invoked directly by `make check` and indirectly by the pre-push hook.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

step() { printf '\n==> %s\n' "$1"; }

step "gofmt"
unformatted="$(gofmt -l cmd)"
if [ -n "$unformatted" ]; then
    echo "Unformatted files (run 'gofmt -w cmd'):" >&2
    echo "$unformatted" >&2
    exit 1
fi

step "go build"
go build ./...

step "go vet"
go vet ./...

step "go test"
go test ./...

step "daemon launcher tests"
bash scripts/test-start-loop.sh

step "tmux hook compatibility tests"
bash scripts/test-tmux-hook-compat.sh

step "tmux conf tests"
bash scripts/test-tmux-conf.sh

step "tmux.rb spec"
if command -v bundle >/dev/null 2>&1 && bundle check >/dev/null 2>&1; then
    bundle exec ruby spec/compiled_config_spec.rb
else
    echo "Ruby gems not installed (run 'bundle install') — skipping (CI will still run it)." >&2
fi

step "shellcheck"
if command -v shellcheck >/dev/null 2>&1; then
    shellcheck scripts/*.sh tmux-ai-attn.tmux
else
    echo "shellcheck not found locally — skipping (CI will still run it)." >&2
fi

echo
echo "All checks passed."
