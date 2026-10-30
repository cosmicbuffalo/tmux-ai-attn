package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTmuxBatchAppliesBatchesLongerThanTmuxAccepts sends one batch far past
// tmux's command-length limit, which tmux refuses whole ("command too long"),
// and checks every option in it arrived.
func TestTmuxBatchAppliesBatchesLongerThanTmuxAccepts(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("", "tat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	if out, err := exec.Command("tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run() })

	const n = 300
	value := strings.Repeat("x", 80)
	var args []string
	for i := 0; i < n; i++ {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-gq", fmt.Sprintf("@tat_%d", i), value)
	}
	if err := tmuxBatch(socket, args); err != nil {
		t.Fatalf("tmuxBatch: %v", err)
	}
	for _, i := range []int{0, n / 2, n - 1} {
		got, err := tmux(socket, "show-options", "-gv", fmt.Sprintf("@tat_%d", i))
		if err != nil || strings.TrimSpace(got) != value {
			t.Fatalf("@tat_%d = %q, %v; want the value set", i, got, err)
		}
	}
}

// TestTmuxBatchRefreshAppliesOptionsWithoutAttachedClient checks that the
// trailing refresh-client, which fails with no client attached, neither loses
// the options set before it nor reports an error.
func TestTmuxBatchRefreshAppliesOptionsWithoutAttachedClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("", "tat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	if out, err := exec.Command("tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run() })

	if err := tmuxBatchRefresh(socket, []string{"set-option", "-gq", "@tat_refresh", "1"}, true); err != nil {
		t.Fatalf("tmuxBatchRefresh: %v", err)
	}
	got, err := tmux(socket, "show-options", "-gv", "@tat_refresh")
	if err != nil || strings.TrimSpace(got) != "1" {
		t.Fatalf("@tat_refresh = %q, %v; want 1", got, err)
	}
}
