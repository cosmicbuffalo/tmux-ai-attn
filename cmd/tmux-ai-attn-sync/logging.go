package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// The daemon runs for days and its log usually lives on a small RAM-backed
// tmpfs (XDG_RUNTIME_DIR). A failure that repeats on every tick must not be
// able to fill it, so every message is clipped, a repeated message is printed
// at most once per repeatInterval, and the log is truncated past maxLogBytes.
const (
	maxLogMessage  = 400
	repeatInterval = 5 * time.Minute
	maxLogBytes    = 1 << 20
)

// diagLog writes rate-limited diagnostics. The zero value is not usable; use
// newDiagLog.
type diagLog struct {
	mu   sync.Mutex
	out  io.Writer
	now  func() time.Time
	seen map[string]*logEntry
}

type logEntry struct {
	printedAt  time.Time
	suppressed int
}

func newDiagLog(out io.Writer) *diagLog {
	return &diagLog{out: out, now: time.Now, seen: map[string]*logEntry{}}
}

var diag = newDiagLog(os.Stderr)

// logf logs one diagnostic line through the shared daemon log.
func logf(format string, args ...any) {
	diag.printf(format, args...)
}

func (l *diagLog) printf(format string, args ...any) {
	msg := clip(strings.TrimSpace(fmt.Sprintf(format, args...)), maxLogMessage)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, e := range l.seen {
		if now.Sub(e.printedAt) > 2*repeatInterval {
			delete(l.seen, k)
		}
	}
	e, ok := l.seen[msg]
	if ok && now.Sub(e.printedAt) < repeatInterval {
		e.suppressed++
		return
	}
	line := msg
	if ok && e.suppressed > 0 {
		line += fmt.Sprintf(" (repeated %d more times since %s)", e.suppressed, e.printedAt.Format("15:04:05"))
	}
	l.seen[msg] = &logEntry{printedAt: now}
	l.limitSize()
	fmt.Fprintf(l.out, "%s %s\n", now.Format(time.RFC3339), line)
}

// limitSize truncates the log once it passes maxLogBytes. The launcher opens
// it in append mode, so writes continue from the start of the file.
func (l *diagLog) limitSize() {
	f, ok := l.out.(*os.File)
	if !ok {
		return
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < maxLogBytes {
		return
	}
	if f.Truncate(0) == nil {
		fmt.Fprintf(f, "%s log truncated at %d bytes\n", l.now().Format(time.RFC3339), info.Size())
	}
}

// clip shortens s to at most n bytes (on a rune boundary), noting how long it was.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return fmt.Sprintf("%s… (%d bytes)", s[:cut], len(s))
}

// describeArgs summarizes a tmux argument list for an error message: the first
// command in full when short, and how many commands and bytes the batch held.
func describeArgs(args []string) string {
	commands, size := 1, 0
	for _, a := range args {
		size += len(a) + 1
		if a == ";" {
			commands++
		}
	}
	first := args
	for i, a := range args {
		if a == ";" {
			first = args[:i]
			break
		}
	}
	desc := clip(strings.Join(first, " "), 120)
	if commands > 1 {
		desc += fmt.Sprintf(" … (%d commands, %d bytes)", commands, size)
	}
	return desc
}
