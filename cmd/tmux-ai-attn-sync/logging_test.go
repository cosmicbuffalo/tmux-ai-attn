package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLog() (*diagLog, *bytes.Buffer, *time.Time) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	l := newDiagLog(&buf)
	l.now = func() time.Time { return now }
	return l, &buf, &now
}

// TestDiagLogSuppressesRepeats checks a message repeated on every tick is
// printed once per repeatInterval, with the count of suppressed copies.
func TestDiagLogSuppressesRepeats(t *testing.T) {
	l, buf, now := testLog()
	for i := 0; i < 100; i++ {
		l.printf("tmux error: boom")
		l.printf("other error")
		*now = now.Add(time.Second)
	}
	if got := strings.Count(buf.String(), "\n"); got != 2 {
		t.Fatalf("want 2 lines for two distinct messages, got %d:\n%s", got, buf)
	}
	*now = now.Add(repeatInterval)
	l.printf("tmux error: boom")
	last := lastLine(buf.String())
	if !strings.Contains(last, "tmux error: boom (repeated 99 more times since 12:00:00)") {
		t.Fatalf("want the repeat count on the next print, got %q", last)
	}
}

func TestDiagLogClipsLongMessages(t *testing.T) {
	l, buf, _ := testLog()
	l.printf("%s", strings.Repeat("x", 20000))
	if buf.Len() > maxLogMessage+100 {
		t.Fatalf("a 20000-byte message logged %d bytes", buf.Len())
	}
	if !strings.Contains(buf.String(), "(20000 bytes)") {
		t.Fatalf("want the original length noted, got %q", buf)
	}
}

// TestDiagLogTruncatesLargeLog checks the daemon empties its own log once it
// passes maxLogBytes, as the launcher opens it in append mode.
func TestDiagLogTruncatesLargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(bytes.Repeat([]byte("x"), maxLogBytes)); err != nil {
		t.Fatal(err)
	}
	l := newDiagLog(f)
	l.printf("after the cap")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 1000 || !strings.Contains(string(data), "log truncated") || !strings.Contains(string(data), "after the cap") {
		t.Fatalf("want a truncated log with the new line, got %d bytes: %.200q", len(data), data)
	}
}

func TestDescribeArgsSummarizesBatches(t *testing.T) {
	var args []string
	for i := 0; i < 200; i++ {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-window-option", "-t", "@1", "-q", "@ai_attn_window_segment", strings.Repeat("y", 50))
	}
	got := describeArgs(args)
	if len(got) > 200 || !strings.Contains(got, "200 commands") {
		t.Fatalf("describeArgs = %q", got)
	}
	if got := describeArgs([]string{"show-options", "-gq"}); got != "show-options -gq" {
		t.Fatalf("a single short command should be shown whole, got %q", got)
	}
}

// TestNoteUpdatePublishesPersistentFailures checks repeated update failures
// reach @ai_attn_last_error and that a later success clears it.
func TestNoteUpdatePublishesPersistentFailures(t *testing.T) {
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
	lastError := func() string {
		out, _ := tmux(socket, "show-options", "-gqv", "@ai_attn_last_error")
		return strings.TrimSpace(out)
	}

	s := &syncer{socket: socket}
	failure := errors.New("tmux set-option … (90 commands, 20000 bytes): command too long")
	for i := 1; i < updateFailuresBeforeError; i++ {
		s.noteUpdate(failure, nil)
		if got := lastError(); got != "" {
			t.Fatalf("after %d failures want no error yet, got %q", i, got)
		}
	}
	s.noteUpdate(failure, nil)
	if got := lastError(); !strings.Contains(got, "command too long") {
		t.Fatalf("want the persistent failure published, got %q", got)
	}

	s.noteUpdate(nil, errors.New("ai-attn list failed"))
	if got := lastError(); got == "" {
		t.Fatal("a success must not clear the error while the ai-attn query is failing")
	}
	s.updateFailures = updateFailuresBeforeError
	s.noteUpdate(nil, nil)
	if got := lastError(); got != "" {
		t.Fatalf("want the error cleared after a successful update, got %q", got)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
