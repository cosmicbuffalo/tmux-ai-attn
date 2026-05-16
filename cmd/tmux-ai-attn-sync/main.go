package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

type listPayload struct {
	Records []record `json:"records"`
}

type record struct {
	Agent     string `json:"agent"`
	PaneID    string `json:"pane_id"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	UpdatedAt int64  `json:"updated_at"`
}

type flashState struct {
	StatefulPanes   map[string]record
	WaitingPanes    map[string]record
	WaitingWindows  map[string]int
	WindowsFlashing map[string]bool
	PanesFlashing   map[string]bool
}

type segmentConfig struct {
	iconWaiting  string
	iconStopped  string
	iconDone     string
	colorWaiting string
	colorStopped string
	colorWorking string
	colorDone    string
	colorFlashBg string
	colorFlashFg string
	colorTextFg  string
}

func optionOr(globals map[string]string, key, fallback string) string {
	if v := globals[key]; v != "" {
		return v
	}
	return fallback
}

func parseSegmentConfig(globals map[string]string) segmentConfig {
	return segmentConfig{
		iconWaiting:  optionOr(globals, "@ai_attn_icon_waiting", "⚠"),
		iconStopped:  optionOr(globals, "@ai_attn_icon_stopped", "⏸"),
		iconDone:     optionOr(globals, "@ai_attn_icon_done", "✓"),
		colorWaiting: optionOr(globals, "@ai_attn_color_waiting", "yellow"),
		colorStopped: optionOr(globals, "@ai_attn_color_stopped", "colour196"),
		colorWorking: optionOr(globals, "@ai_attn_color_working", "#d88786"),
		colorDone:    optionOr(globals, "@ai_attn_color_done", "green"),
		colorFlashBg: optionOr(globals, "@ai_attn_color_flash_bg", "colour226"),
		colorFlashFg: optionOr(globals, "@ai_attn_color_flash_fg", "colour16"),
		colorTextFg:  optionOr(globals, "@ai_attn_color_text_fg", "colour255"),
	}
}

func renderWindowSegment(windowState string, flashing bool, flashPhase string, spinnerFrame string, cfg segmentConfig) string {
	if windowState == "" {
		return ""
	}
	var b strings.Builder
	isFlashOn := flashing && flashPhase == "1"

	if isFlashOn {
		b.WriteString("#[fg=")
		b.WriteString(cfg.colorFlashFg)
		b.WriteString("]#[bg=")
		b.WriteString(cfg.colorFlashBg)
		b.WriteString("] ")
	} else {
		b.WriteString(" ")
	}

	var iconColor, icon string
	switch windowState {
	case "waiting":
		iconColor = cfg.colorWaiting
		icon = cfg.iconWaiting
	case "stopped":
		iconColor = cfg.colorStopped
		icon = cfg.iconStopped
	case "working":
		iconColor = cfg.colorWorking
		icon = spinnerFrame
	case "done":
		iconColor = cfg.colorDone
		icon = cfg.iconDone
	}

	if !isFlashOn {
		b.WriteString("#[fg=")
		b.WriteString(iconColor)
		b.WriteString("]")
	}
	b.WriteString(icon)
	b.WriteString(" ")

	if isFlashOn {
		b.WriteString("#[fg=")
		b.WriteString(cfg.colorFlashFg)
		b.WriteString("]")
	} else {
		b.WriteString("#[fg=")
		b.WriteString(cfg.colorTextFg)
		b.WriteString("]")
	}

	return b.String()
}

const debounceDuration = 50 * time.Millisecond

var defaultSpinnerFrames = []string{"·", "·", "✢", "✳", "✶", "✽", "✻", "✻", "✻", "✽", "✶", "✳", "✢", "·"}

func parseSpinnerFrames(globals map[string]string) []string {
	v := globals["@ai_attn_spinner_frames"]
	if v == "" {
		return defaultSpinnerFrames
	}
	raw := strings.Split(v, ",")
	frames := make([]string, 0, len(raw))
	for _, f := range raw {
		if f != "" {
			frames = append(frames, f)
		}
	}
	if len(frames) == 0 {
		return defaultSpinnerFrames
	}
	return frames
}

func computeSpinnerFrame(frames []string, tickMs int) string {
	if tickMs <= 0 {
		tickMs = 120
	}
	ms := time.Now().UnixMilli()
	return frames[(ms/int64(tickMs))%int64(len(frames))]
}

// emptyStateHash is precomputed once since it's a constant — avoids
// recomputing SHA-256 on every idle syncOnce cycle.
var emptyStateHash = stateHash(nil, nil, nil, nil, nil)

// main is the program entry point; it delegates to run and exits with its return code.
func main() {
	os.Exit(run())
}

// syncer holds the mutable state threaded through successive syncOnce calls.
type syncer struct {
	socket               string
	cachedPayload        listPayload
	hasCached            bool
	windowFlashStartedAt map[string]int64
	flashPhaseCounter    int
	flashTickCounter     int
}

// run parses flags, sets up fsnotify and signal handling, and enters the main sync loop.
// Called from main; returns the process exit code.
func run() int {
	var socket, pidfile string
	var clear bool
	flag.StringVar(&socket, "tmux-socket", "", "tmux socket path")
	flag.StringVar(&pidfile, "pidfile", "", "PID file path (removed on exit)")
	flag.BoolVar(&clear, "clear", false, "clear all ai-attn tmux options and exit")
	flag.Parse()
	if socket == "" {
		fmt.Fprintln(os.Stderr, "missing --tmux-socket")
		return 2
	}

	if clear {
		clearAllAttnOptions(socket)
		return 0
	}

	// Write our own PID file (atomically) and clean up on exit.
	if pidfile != "" {
		if err := writePIDFile(pidfile); err != nil {
			fmt.Fprintf(os.Stderr, "pidfile: %v\n", err)
			return 1
		}
		defer os.Remove(pidfile)
	}

	// Handle signals for clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Set up fsnotify watcher on the ai-attn state directory.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fsnotify: %v\n", err)
		return 1
	}
	defer watcher.Close()

	dir := aiAttnStateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", dir, err)
		return 1
	}
	if err := watcher.Add(dir); err != nil {
		fmt.Fprintf(os.Stderr, "watch %s: %v\n", dir, err)
		return 1
	}

	// Initial sync.
	s := &syncer{socket: socket}
	serverGone, tick := s.syncOnce(true)
	if serverGone {
		return 0
	}

	// Adaptive tick timer for display refresh (dots animation, age text).
	var tickTimer *time.Timer
	var tickCh <-chan time.Time
	resetTick := func(d time.Duration) {
		if tickTimer != nil {
			if !tickTimer.Stop() {
				// Drain the channel if the timer already fired.
				select {
				case <-tickTimer.C:
				default:
				}
			}
		}
		if d > 0 {
			if tickTimer != nil {
				tickTimer.Reset(d)
			} else {
				tickTimer = time.NewTimer(d)
			}
			tickCh = tickTimer.C
		} else {
			tickTimer = nil
			tickCh = nil
		}
	}
	resetTick(tick)

	// Debounce rapid fsnotify events (e.g. create+write for same file).
	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	for {
		select {
		case <-sigCh:
			clearAllAttnOptions(socket)
			return 0

		case event, ok := <-watcher.Events:
			if !ok {
				return 0
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) != 0 {
				if debounceTimer != nil {
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer.Reset(debounceDuration)
				} else {
					debounceTimer = time.NewTimer(debounceDuration)
				}
				debounceCh = debounceTimer.C
			}

		case <-debounceCh:
			debounceTimer = nil
			debounceCh = nil
			serverGone, tick = s.syncOnce(true)
			if serverGone {
				return 0
			}
			resetTick(tick)

		case <-tickCh:
			serverGone, tick = s.syncOnce(false)
			if serverGone {
				return 0
			}
			resetTick(tick)

		case err, ok := <-watcher.Errors:
			if !ok {
				return 0
			}
			fmt.Fprintf(os.Stderr, "fsnotify error: %v\n", err)
		}
	}
}

// aiAttnStateDir returns the directory where ai-attn stores its state files.
// Uses AI_ATTN_STATE_DIR env var if set, otherwise defaults to ~/.local/state/ai-attn.
func aiAttnStateDir() string {
	if v := os.Getenv("AI_ATTN_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "state", "ai-attn")
}

// syncOnce performs a single sync cycle. It returns whether the tmux server is
// gone and the recommended tick interval for the next display refresh (0 means
// no tick is needed because no state is being displayed).
func (s *syncer) syncOnce(refreshQuery bool) (serverGone bool, tick time.Duration) {
	globals, err := tmuxGetGlobalOptions(s.socket, "@ai_attn_")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmux error: %v\n", err)
		if _, statErr := os.Stat(s.socket); statErr != nil {
			return true, 0 // socket gone, exit
		}
		return false, time.Second // retry
	}

	nowUnix := time.Now().Unix()

	attnCLIPath := globals["@ai_attn_cli"]
	if attnCLIPath == "" {
		attnCLIPath = "ai-attn"
	}
	segCfg := parseSegmentConfig(globals)
	spinFrames := parseSpinnerFrames(globals)
	tickMs := 120
	if v, parseErr := strconv.Atoi(globals["@ai_attn_tick_ms"]); parseErr == nil && v > 0 {
		tickMs = v
	}
	flashMultiplier := 4
	if v, parseErr := strconv.Atoi(globals["@ai_attn_flash_multiplier"]); parseErr == nil && v > 0 {
		flashMultiplier = v
	}
	flashGraceSeconds := int64(3)
	if v, parseErr := strconv.ParseInt(globals["@ai_attn_seen_flash_seconds"], 10, 64); parseErr == nil && v > 0 {
		flashGraceSeconds = v
	}

	payload := s.cachedPayload
	queryErr := error(nil)
	if refreshQuery || !s.hasCached {
		payload, queryErr = queryAttn(attnCLIPath)
		if queryErr != nil {
			_ = tmuxSetGlobal(s.socket, "@ai_attn_last_error", queryErr.Error())
			payload = listPayload{}
		}
		s.cachedPayload = payload
		s.hasCached = true
	}

	// Fast path: if no records have a non-empty state, check whether
	// the previous cycle was also idle. If so, skip the expensive tmux
	// topology reads (list-panes, list-clients).
	hasRecordsWithState := false
	for _, r := range payload.Records {
		if r.State != "" {
			hasRecordsWithState = true
			break
		}
	}
	if !hasRecordsWithState {
		emptyHash := emptyStateHash
		clearingStaleError := refreshQuery && queryErr == nil && globals["@ai_attn_last_error"] != ""
		currentHash := globals["@ai_attn_state_hash"]
		currentPhase := globals["@ai_attn_flash_phase"]
		if currentHash == emptyHash && currentPhase == "0" && !clearingStaleError {
			return false, 0
		}
	}

	paneLines, err := tmux(s.socket, "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}\t#{@ai_attn_window_seen_at}")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false, 0
	}

	paneToWindow := map[string]string{}
	panes := map[string]struct{}{}
	windows := map[string]struct{}{}
	seenAtByWindow := map[string]int64{}

	for _, line := range strings.Split(strings.TrimSpace(paneLines), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		paneID := parts[0]
		windowID := parts[1]
		paneToWindow[paneID] = windowID
		panes[paneID] = struct{}{}
		if _, seen := windows[windowID]; !seen {
			windows[windowID] = struct{}{}
			seenAt := int64(0)
			if len(parts) == 3 {
				if v, parseErr := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); parseErr == nil {
					seenAt = v
				}
			}
			seenAtByWindow[windowID] = seenAt
		}
	}

	clientWindowLines, err := tmux(s.socket, "list-clients", "-F", "#{window_id}")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false, 0
	}
	activeWindows := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(clientWindowLines), "\n") {
		windowID := strings.TrimSpace(line)
		if windowID == "" {
			continue
		}
		activeWindows[windowID] = struct{}{}
	}

	state, nextSeenAtByWindow := buildFlashState(payload.Records, paneToWindow, windows, activeWindows, seenAtByWindow, nowUnix, flashGraceSeconds)

	// Persist window flash for the full grace period once started,
	// but only while the window still has waiting panes.
	if s.windowFlashStartedAt == nil {
		s.windowFlashStartedAt = map[string]int64{}
	}
	// Drop entries for windows that no longer exist so the map can't grow
	// unbounded across sessions with churning window IDs.
	for windowID := range s.windowFlashStartedAt {
		if _, ok := windows[windowID]; !ok {
			delete(s.windowFlashStartedAt, windowID)
		}
	}
	for windowID := range windows {
		if state.WaitingWindows[windowID] == 0 {
			delete(s.windowFlashStartedAt, windowID)
			continue
		}
		if state.WindowsFlashing[windowID] {
			if _, tracked := s.windowFlashStartedAt[windowID]; !tracked {
				s.windowFlashStartedAt[windowID] = nowUnix
			}
		}
		if startedAt, tracked := s.windowFlashStartedAt[windowID]; tracked {
			if nowUnix-startedAt < flashGraceSeconds {
				state.WindowsFlashing[windowID] = true
			} else {
				delete(s.windowFlashStartedAt, windowID)
			}
		}
	}

	windowStates := computeWindowStates(state.StatefulPanes, paneToWindow)

	nextStateHash := stateHash(state.StatefulPanes, state.WaitingPanes, state.WaitingWindows, state.WindowsFlashing, state.PanesFlashing)
	currentStateHash := globals["@ai_attn_state_hash"]

	anyWindowFlashing := false
	for _, flashing := range state.WindowsFlashing {
		if flashing {
			anyWindowFlashing = true
			break
		}
	}

	hasWorkingPane := false
	for _, rec := range state.StatefulPanes {
		if rec.State == "working" {
			hasWorkingPane = true
			break
		}
	}

	needsFlashToggle := len(state.WaitingPanes) > 0 || anyWindowFlashing
	nextFlashPhase := s.advanceFlashPhase(needsFlashToggle, hasWorkingPane, flashMultiplier)
	currentFlashPhase := globals["@ai_attn_flash_phase"]

	spinnerFrame := ""
	if hasWorkingPane {
		spinnerFrame = computeSpinnerFrame(spinFrames, tickMs)
	}

	clearingStaleError := refreshQuery && queryErr == nil && globals["@ai_attn_last_error"] != ""
	visualChanged := nextStateHash != currentStateHash || nextFlashPhase != currentFlashPhase
	if !visualChanged && !clearingStaleError && !hasWorkingPane {
		return false, tickForState(state, tickMs, flashMultiplier)
	}

	// Batch all updates into a single tmux call.
	var batch []string
	for windowID, nextSeenAt := range nextSeenAtByWindow {
		if nextSeenAt != seenAtByWindow[windowID] {
			batch = appendWindowOption(batch, windowID, "@ai_attn_window_seen_at", strconv.FormatInt(nextSeenAt, 10))
		}
	}
	for windowID := range windows {
		count := state.WaitingWindows[windowID]
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_waiting", boolString(count > 0))
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_waiting_count", strconv.Itoa(count))
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_flash", boolString(state.WindowsFlashing[windowID]))
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_state", windowStates[windowID])
		segment := renderWindowSegment(windowStates[windowID], state.WindowsFlashing[windowID], nextFlashPhase, spinnerFrame, segCfg)
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_segment", segment)
	}

	for paneID := range panes {
		if item, ok := state.StatefulPanes[paneID]; ok {
			batch = appendPaneState(batch, paneID, item.State, item.Agent, item.Reason, item.UpdatedAt, state.PanesFlashing[paneID])
		} else {
			batch = appendPaneState(batch, paneID, "", "", "", 0, false)
		}
	}

	if refreshQuery && queryErr == nil {
		batch = appendGlobalOption(batch, "@ai_attn_last_error", "")
	}
	batch = appendGlobalOption(batch, "@ai_attn_any_waiting", boolString(len(state.WaitingPanes) > 0))
	batch = appendGlobalOption(batch, "@ai_attn_waiting_panes", strconv.Itoa(len(state.WaitingPanes)))
	batch = appendGlobalOption(batch, "@ai_attn_waiting_windows", strconv.Itoa(len(state.WaitingWindows)))
	batch = appendGlobalOption(batch, "@ai_attn_flash_phase", nextFlashPhase)
	batch = appendGlobalOption(batch, "@ai_attn_state_hash", nextStateHash)
	batch = appendGlobalOption(batch, "@ai_attn_spinner_frame", spinnerFrame)

	if err := tmuxBatch(s.socket, batch); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if isTruthy(strings.ToLower(globals["@ai_attn_refresh_client"])) {
		if err := tmuxRefreshClientBestEffort(s.socket); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}

	return false, tickForState(state, tickMs, flashMultiplier)
}

// advanceFlashPhase advances the flash-phase state machine by one tick and
// returns the next phase ("0" or "1"). When a working pane is present the
// syncer ticks at @ai_attn_tick_ms cadence; phase toggling is gated by
// flashMultiplier so the flash animation stays at ~tickMs*flashMultiplier
// regardless. When no flash is needed the per-tick counter resets so future
// flash bursts start from a predictable phase.
func (s *syncer) advanceFlashPhase(needsFlashToggle, hasWorkingPane bool, flashMultiplier int) string {
	if !needsFlashToggle {
		s.flashTickCounter = 0
		return "0"
	}
	if flashMultiplier <= 0 {
		flashMultiplier = 1
	}
	s.flashTickCounter++
	if !hasWorkingPane || s.flashTickCounter%flashMultiplier == 0 {
		s.flashPhaseCounter++
	}
	return strconv.Itoa(s.flashPhaseCounter % 2)
}

// computeWindowStates computes the highest-priority state per window
// from the set of stateful panes. Priority: waiting > working > stopped > done.
func computeWindowStates(statefulPanes map[string]record, paneToWindow map[string]string) map[string]string {
	priority := map[string]int{"waiting": 4, "working": 3, "stopped": 2, "done": 1}
	states := map[string]string{}
	for paneID, item := range statefulPanes {
		windowID := paneToWindow[paneID]
		if priority[item.State] > priority[states[windowID]] {
			states[windowID] = item.State
		}
	}
	return states
}

// tickForState returns the display tick interval based on active states.
// Working panes tick at tickMs for spinner animation, flashing panes tick at
// tickMs * flashMultiplier for flash animation, other stateful panes need 10s
// for age text updates, and idle needs no ticking at all.
func tickForState(state flashState, tickMs int, flashMultiplier int) time.Duration {
	spinnerTick := time.Duration(tickMs) * time.Millisecond
	flashTick := time.Duration(tickMs*flashMultiplier) * time.Millisecond
	for _, rec := range state.StatefulPanes {
		if rec.State == "working" {
			return spinnerTick
		}
	}
	for _, flashing := range state.PanesFlashing {
		if flashing {
			return flashTick
		}
	}
	for _, flashing := range state.WindowsFlashing {
		if flashing {
			return flashTick
		}
	}
	if len(state.StatefulPanes) > 0 {
		return 10 * time.Second
	}
	return 0
}

// buildFlashState computes the current attention/flash state for all panes and windows
// from the given records, topology, and timing information. Called from syncOnce.
func buildFlashState(records []record, paneToWindow map[string]string, windows map[string]struct{}, activeWindows map[string]struct{}, seenAtByWindow map[string]int64, nowUnix int64, flashGraceSeconds int64) (flashState, map[string]int64) {
	// For each target pane, keep the most recent record with a non-empty
	// state.  This ensures a newer state from any source overrides an
	// older one.
	latestByPane := map[string]record{}
	for _, rec := range records {
		if rec.State == "" {
			continue
		}
		targetPaneID := rec.PaneID
		if targetPaneID == "" {
			continue
		}
		if _, ok := paneToWindow[targetPaneID]; !ok {
			continue
		}
		if prev, exists := latestByPane[targetPaneID]; !exists || rec.UpdatedAt > prev.UpdatedAt {
			latestByPane[targetPaneID] = rec
		}
	}

	// statefulPanes: all panes with any non-empty state (for display).
	statefulPanes := map[string]record{}
	for paneID, rec := range latestByPane {
		statefulPanes[paneID] = rec
	}

	// waitingPanes: only panes with state="waiting" (for flash/attention).
	waitingPanes := map[string]record{}
	waitingWindows := map[string]int{}
	waitingWindowUpdatedAt := map[string]int64{}
	for paneID, rec := range latestByPane {
		if rec.State != "waiting" {
			continue
		}
		waitingPanes[paneID] = rec
		windowID := paneToWindow[paneID]
		waitingWindows[windowID]++
		if rec.UpdatedAt > waitingWindowUpdatedAt[windowID] {
			waitingWindowUpdatedAt[windowID] = rec.UpdatedAt
		}
	}

	nextSeenAtByWindow := map[string]int64{}
	windowFlash := map[string]bool{}
	for windowID := range windows {
		newestWaitingAt := waitingWindowUpdatedAt[windowID]
		seenAt := seenAtByWindow[windowID]
		if newestWaitingAt == 0 {
			windowFlash[windowID] = false
			nextSeenAtByWindow[windowID] = 0
			continue
		}
		if _, active := activeWindows[windowID]; active && newestWaitingAt > seenAt && nowUnix-newestWaitingAt >= flashGraceSeconds {
			seenAt = newestWaitingAt
		}
		nextSeenAtByWindow[windowID] = seenAt
		windowFlash[windowID] = newestWaitingAt > seenAt
	}

	paneFlash := map[string]bool{}
	for paneID, item := range waitingPanes {
		windowID := paneToWindow[paneID]
		paneFlash[paneID] = windowFlash[windowID] && nowUnix-item.UpdatedAt < flashGraceSeconds
	}

	return flashState{
		StatefulPanes:   statefulPanes,
		WaitingPanes:    waitingPanes,
		WaitingWindows:  waitingWindows,
		WindowsFlashing: windowFlash,
		PanesFlashing:   paneFlash,
	}, nextSeenAtByWindow
}

// queryAttn invokes the ai-attn CLI to fetch the current attention records as JSON.
// Called from syncOnce when refreshQuery is true.
func queryAttn(cli string) (listPayload, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "list", "--json")
	out, err := cmd.Output()
	if err != nil {
		return listPayload{}, err
	}
	var payload listPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return listPayload{}, err
	}
	return payload, nil
}

// tmux runs a tmux command against the given socket with a 5-second timeout.
// Used by all other tmux helper functions (tmuxBatch, tmuxSetGlobal, etc.).
func tmux(socket string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("tmux %s: timed out after 5s", strings.Join(args, " "))
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// tmuxBatch runs multiple tmux commands in a single invocation using ";"
// separators. The args slice should contain command tokens separated by ";"
// elements, e.g.: ["set-option", "-gq", "@foo", "bar", ";", "set-option", "-gq", "@baz", "qux"]
func tmuxBatch(socket string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	_, err := tmux(socket, args...)
	return err
}

// tmuxRefreshClientBestEffort asks tmux to redraw the status bar, silently ignoring
// "no current client" errors. Called from syncOnce when @ai_attn_refresh_client is on.
func tmuxRefreshClientBestEffort(socket string) error {
	_, err := tmux(socket, "refresh-client", "-S")
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "no current client") {
		return nil
	}
	return err
}

// appendGlobalOption appends a "set-option -gq" command to the batch for a global tmux option.
func appendGlobalOption(batch []string, name, value string) []string {
	if len(batch) > 0 {
		batch = append(batch, ";")
	}
	return append(batch, "set-option", "-gq", name, value)
}

// appendWindowOption appends a "set-window-option" command to the batch for a per-window tmux option.
func appendWindowOption(batch []string, target, name, value string) []string {
	if len(batch) > 0 {
		batch = append(batch, ";")
	}
	return append(batch, "set-window-option", "-t", target, "-q", name, value)
}

// appendPaneOption appends a "set-option -pt" command to the batch for a per-pane tmux option.
func appendPaneOption(batch []string, target, name, value string) []string {
	if len(batch) > 0 {
		batch = append(batch, ";")
	}
	return append(batch, "set-option", "-pt", target, "-q", name, value)
}

// appendPaneState appends all per-pane @ai_attn_pane_* option commands to the batch.
// Called from syncOnce and clearAllAttnOptions to set or clear pane-level state.
func appendPaneState(batch []string, paneID string, state, agent, reason string, updatedAt int64, flash bool) []string {
	batch = appendPaneOption(batch, paneID, "@ai_attn_pane_state", state)
	batch = appendPaneOption(batch, paneID, "@ai_attn_pane_agent", agent)
	batch = appendPaneOption(batch, paneID, "@ai_attn_pane_reason", reason)
	batch = appendPaneOption(batch, paneID, "@ai_attn_pane_updated_at", strconv.FormatInt(updatedAt, 10))
	batch = appendPaneOption(batch, paneID, "@ai_attn_pane_flash", boolString(flash))
	return batch
}

// tmuxSetGlobal sets a single global tmux option. Called from syncOnce to record errors.
func tmuxSetGlobal(socket, name, value string) error {
	_, err := tmux(socket, "set-option", "-gq", name, value)
	return err
}

// tmuxGetGlobalOptions fetches all global user options matching the given prefix
// in a single tmux call and returns them as a map of name->value.
func tmuxGetGlobalOptions(socket, prefix string) (map[string]string, error) {
	out, err := tmux(socket, "show-options", "-gq")
	if err != nil {
		return nil, err
	}
	options := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		name := parts[0]
		value := ""
		if len(parts) == 2 {
			value = strings.TrimPrefix(strings.TrimSuffix(parts[1], "\""), "\"")
		}
		options[name] = value
	}
	return options, nil
}

// boolString converts a bool to "1" or "0" for use in tmux option values.
func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// isTruthy returns true if the string represents a truthy value (1, on, yes, true).
func isTruthy(value string) bool {
	switch value {
	case "1", "on", "yes", "true":
		return true
	default:
		return false
	}
}

// clearAllAttnOptions resets all @ai_attn_* global, per-window, and per-pane
// options on graceful shutdown so tmux doesn't show stale attention indicators.
func clearAllAttnOptions(socket string) {
	globals, err := tmuxGetGlobalOptions(socket, "@ai_attn_")
	if err != nil {
		return
	}
	var batch []string
	for name := range globals {
		// Preserve configuration options; only clear runtime state.
		switch name {
		case "@ai_attn_cli", "@ai_attn_version",
			"@ai_attn_seen_flash_seconds", "@ai_attn_refresh_client",
			"@ai_attn_dev_build", "@ai_attn_auto_install",
			"@ai_attn_enable_default_formats",
			"@ai_attn_icon_waiting", "@ai_attn_icon_stopped", "@ai_attn_icon_done",
			"@ai_attn_color_waiting", "@ai_attn_color_stopped", "@ai_attn_color_working",
			"@ai_attn_color_done", "@ai_attn_color_flash_bg", "@ai_attn_color_flash_fg",
			"@ai_attn_color_text_fg", "@ai_attn_spinner_frames",
			"@ai_attn_tick_ms", "@ai_attn_flash_multiplier":
			continue
		}
		batch = appendGlobalOption(batch, name, "")
	}

	// Also clear per-window and per-pane options to prevent stale styling.
	paneLines, paneErr := tmux(socket, "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}")
	if paneErr == nil {
		clearedWindows := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(paneLines), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) < 2 {
				continue
			}
			paneID, windowID := parts[0], parts[1]
			// Clear per-pane options.
			batch = appendPaneState(batch, paneID, "", "", "", 0, false)
			// Clear per-window options (once per window).
			if !clearedWindows[windowID] {
				clearedWindows[windowID] = true
				batch = appendWindowOption(batch, windowID, "@ai_attn_window_waiting", "0")
				batch = appendWindowOption(batch, windowID, "@ai_attn_window_waiting_count", "0")
				batch = appendWindowOption(batch, windowID, "@ai_attn_window_flash", "0")
				batch = appendWindowOption(batch, windowID, "@ai_attn_window_state", "")
				batch = appendWindowOption(batch, windowID, "@ai_attn_window_segment", "")
			}
		}
	}

	if len(batch) > 0 {
		_ = tmuxBatch(socket, batch)
	}
}

// writePIDFile writes the current process PID to the given path.
func writePIDFile(path string) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
}

// stateHash computes a change-detection fingerprint for the current state.
// Intentionally excludes Reason and UpdatedAt: these are informational fields
// that change frequently but don't affect the structural state (which panes are
// active/waiting, which windows are flashing). Including them would cause
// unnecessary tmux batch writes on every tick.
func stateHash(activePanes map[string]record, waitingPanes map[string]record, waitingWindows map[string]int, windowFlash map[string]bool, paneFlash map[string]bool) string {
	activeParts := make([]string, 0, len(activePanes))
	for paneID, item := range activePanes {
		activeParts = append(activeParts, paneID+":"+item.State+":"+item.Agent)
	}
	sort.Strings(activeParts)

	waitingPaneParts := make([]string, 0, len(waitingPanes))
	for paneID, item := range waitingPanes {
		waitingPaneParts = append(waitingPaneParts, paneID+":"+item.Agent+":"+boolString(paneFlash[paneID]))
	}
	sort.Strings(waitingPaneParts)

	windowParts := make([]string, 0, len(waitingWindows))
	for windowID, count := range waitingWindows {
		windowParts = append(windowParts, windowID+":"+strconv.Itoa(count)+":"+boolString(windowFlash[windowID]))
	}
	sort.Strings(windowParts)

	fingerprint := strings.Join([]string{
		"active=" + strings.Join(activeParts, ","),
		"panes=" + strings.Join(waitingPaneParts, ","),
		"windows=" + strings.Join(windowParts, ","),
	}, "|")

	return fingerprint
}
