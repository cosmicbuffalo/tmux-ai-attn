#!/usr/bin/env bash
# Git pre-push hook. Runs the full check suite only when the push includes
# main; other branches push without checks. Bypass with: git push --no-verify
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"

# Pre-push receives lines on stdin: <local_ref> <local_sha> <remote_ref> <remote_sha>
pushing_main=0
while read -r _local_ref _local_sha remote_ref _remote_sha; do
    if [ "$remote_ref" = "refs/heads/main" ]; then
        pushing_main=1
        break
    fi
done

if [ "$pushing_main" -eq 0 ]; then
    echo "pre-push: main not in this push, skipping checks."
    exit 0
fi

echo "pre-push: main is being pushed, running checks (bypass with --no-verify)..."
exec bash "$repo_root/scripts/check.sh"
