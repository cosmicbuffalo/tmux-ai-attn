.PHONY: build test format lint check clean install-hooks

CMD := ./cmd/tmux-ai-attn-sync

build:
	go build $(CMD)

test:
	go test ./...

format:
	gofmt -w .

lint:
	go vet ./...

check:
	@bash scripts/check.sh

clean:
	rm -f tmux-ai-attn-sync bin/tmux-ai-attn-sync bin/.version

install-hooks:
	@hooks_dir="$$(git rev-parse --git-path hooks)"; \
	mkdir -p "$$hooks_dir"; \
	{ \
		echo '#!/usr/bin/env bash'; \
		echo 'exec bash "$$(git rev-parse --show-toplevel)/scripts/pre-push.sh" "$$@"'; \
	} > "$$hooks_dir/pre-push"; \
	chmod +x "$$hooks_dir/pre-push"; \
	echo "Installed pre-push hook at $$hooks_dir/pre-push"; \
	echo "Hook runs the check suite when pushing main; bypass with: git push --no-verify"
