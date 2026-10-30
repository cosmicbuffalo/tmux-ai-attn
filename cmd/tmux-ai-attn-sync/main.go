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
	"slices"
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
	Socket    string `json:"socket"`
	CWD       string `json:"cwd"`
}

type paneIdentity struct {
	Command string
	CWD     string
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
	// frameRef: a working window's segment shows the spinner frame by
	// reference, as #{@ai_attn_spinner_frame}, so a spinner tick only writes
	// that one global option. Only safe when every format reading the
	// segment expands it; see segmentReadersExpand.
	frameRef bool
}

// spinnerFrameRef is how a segment refers to the current spinner frame.
const spinnerFrameRef = "#{@ai_attn_spinner_frame}"

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
		if cfg.frameRef {
			icon = spinnerFrameRef
		}
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
	} else if cfg.colorTextFg == "default" {
		b.WriteString("#[default]")
	} else {
		b.WriteString("#[fg=")
		b.WriteString(cfg.colorTextFg)
		b.WriteString("]")
	}

	return b.String()
}

const (
	debounceDuration        = 50 * time.Millisecond
	idleServerCheckInterval = 30 * time.Second
)

var defaultSpinnerFrames = []string{"·", "·", "✢", "✳", "✶", "✽", "✻", "✻", "✻", "✽", "✶", "✳", "✢", "·"}

func loopTickInterval(displayTick time.Duration) time.Duration {
	if displayTick <= 0 {
		return idleServerCheckInterval
	}
	return displayTick
}

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
	// updateFailures counts consecutive syncs whose tmux option update failed.
	updateFailures int
	// globals, topo, and activeWindows cache the last tmux reads, taken at
	// readAt, so animation ticks can reuse them instead of spawning three tmux
	// clients each. nil means the next sync reads them again.
	globals       map[string]string
	topo          *paneTopology
	activeWindows map[string]struct{}
	// clients and focused, read with activeWindows: how many clients are
	// attached, and whether any of their terminals has focus.
	clients int
	focused bool
	readAt  time.Time
	// windowFormatsRaw caches whether a format set on a single window reads
	// @ai_attn_window_segment without expanding it, checked for the windows
	// in windowFormatsKey at windowFormatsAt.
	windowFormatsRaw bool
	windowFormatsKey string
	windowFormatsAt  time.Time
	// segmentsWritten says whether this daemon has written the window
	// segments yet, and segmentsFrameRef how they show the spinner frame.
	segmentsWritten  bool
	segmentsFrameRef bool
}

// windowFormatsMaxAge bounds how long the check of formats set on single
// windows is reused while the windows stay the same.
const windowFormatsMaxAge = 10 * time.Second

// tmuxReadMaxAge bounds how long animation ticks reuse cached tmux reads, so
// option edits and window, pane, or client changes are picked up within it.
const tmuxReadMaxAge = time.Second

// pausedTick is how often an animation ticks while no client is attached:
// nothing is seen, so it only needs to resume soon after one attaches. State
// changes still sync at once, from ai-attn's notifications.
const pausedTick = 5 * time.Second

// unfocusedTick is the fastest an animation ticks while no attached
// terminal has focus.
const unfocusedTick = time.Second

// displayPace is how fast the status line can usefully animate. See paceFor.
type displayPace struct {
	// paused: no client is attached, so no animation is seen.
	paused bool
	// minTick: animation ticks no faster than this.
	minTick time.Duration
}

// paceFor works out the display pace from the options and clients tmux
// reported. With no client attached nothing is seen. Without
// @ai_attn_refresh_client, tmux redraws the status line only every
// status-interval seconds (never, at 0), so faster ticks write frames nobody
// sees. And when tmux tracks focus (focus-events on) and no attached
// terminal has it, a frame a second is enough.
func paceFor(globals map[string]string, clients int, focused bool) displayPace {
	if clients == 0 {
		return displayPace{paused: true}
	}
	var pace displayPace
	if !isTruthy(strings.ToLower(globals["@ai_attn_refresh_client"])) {
		seconds, err := strconv.Atoi(globals["status-interval"])
		if err != nil || seconds <= 0 {
			return displayPace{paused: true}
		}
		pace.minTick = time.Duration(seconds) * time.Second
	}
	if globals["focus-events"] == "on" && !focused && pace.minTick < unfocusedTick {
		pace.minTick = unfocusedTick
	}
	return pace
}

// updateFailuresBeforeError is how many consecutive failed updates it takes
// to report the failure in @ai_attn_last_error. A lone failure (a busy server
// timing out) is only logged.
const updateFailuresBeforeError = 3

// noteUpdate records the outcome of a sync's tmux option update. A failure
// that persists is published in @ai_attn_last_error, where status lines and
// pmux already look, instead of only in the daemon log; the next successful
// update clears it unless the ai-attn query failed this cycle.
func (s *syncer) noteUpdate(err, queryErr error) {
	if err == nil {
		if s.updateFailures >= updateFailuresBeforeError && queryErr == nil {
			if setErr := tmuxSetGlobal(s.socket, "@ai_attn_last_error", ""); setErr != nil {
				logf("%v", setErr)
			}
		}
		if s.updateFailures > 0 {
			logf("tmux option updates recovered after %d failed syncs", s.updateFailures)
		}
		s.updateFailures = 0
		return
	}
	s.updateFailures++
	logf("%v", err)
	if s.updateFailures == updateFailuresBeforeError {
		msg := clip("tmux-ai-attn-sync cannot update tmux options: "+err.Error(), 200)
		if setErr := tmuxSetGlobal(s.socket, "@ai_attn_last_error", msg); setErr != nil {
			logf("%v", setErr)
		}
	}
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
			logf("pidfile: %v", err)
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
		logf("fsnotify: %v", err)
		return 1
	}
	defer watcher.Close()

	dir := aiAttnStateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf("mkdir %s: %v", dir, err)
		return 1
	}
	if err := watcher.Add(dir); err != nil {
		logf("watch %s: %v", dir, err)
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
		// Even an idle daemon must periodically notice that its tmux server
		// exited. Without this floor there may be no fsnotify event or display
		// tick to wake the loop, leaving the process and PID file behind.
		d = loopTickInterval(d)
		if d > 0 {
			if tickTimer != nil {
				tickTimer.Reset(d)
			} else {
				tickTimer = time.NewTimer(d)
			}
			tickCh = tickTimer.C
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
			logf("fsnotify error: %v", err)
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

func tmuxServerIsGone(socket string, err error) bool {
	if _, statErr := os.Stat(socket); os.IsNotExist(statErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no server running",
		"failed to connect to server",
		"connection refused",
		"no such file or directory",
		"server exited unexpectedly",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// syncOnce performs a single sync cycle. It returns whether the tmux server is
// gone and the recommended tick interval for the next display refresh (0 means
// no tick is needed because no state is being displayed).
func (s *syncer) syncOnce(refreshQuery bool) (serverGone bool, tick time.Duration) {
	// Animation ticks within tmuxReadMaxAge of the last read reuse it; an
	// ai-attn change or a stale read starts over from tmux.
	if refreshQuery || s.globals == nil || time.Since(s.readAt) >= tmuxReadMaxAge {
		s.globals, s.topo, s.activeWindows = nil, nil, nil
		globals, err := tmuxGetGlobalOptions(s.socket, "@ai_attn_")
		if err != nil {
			logf("tmux error: %v", err)
			if tmuxServerIsGone(s.socket, err) {
				return true, 0
			}
			return false, time.Second // retry
		}
		s.globals, s.readAt = globals, time.Now()
	}
	globals := s.globals

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

	if s.topo == nil {
		paneLines, err := tmux(s.socket, "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}\t#{@ai_attn_window_seen_at}\t#{pane_current_command}\t#{pane_current_path}")
		if err != nil {
			logf("%v", err)
			return false, 0
		}
		clientLines, err := tmux(s.socket, "list-clients", "-F", "#{window_id}\t#{client_flags}")
		if err != nil {
			logf("%v", err)
			return false, 0
		}
		activeWindows, clients, focused := parseClientLines(clientLines)
		topo := parsePaneLines(paneLines)
		s.topo, s.activeWindows, s.clients, s.focused = &topo, activeWindows, clients, focused
		if key := windowsKey(topo.windows); key != s.windowFormatsKey || time.Since(s.windowFormatsAt) >= windowFormatsMaxAge {
			raw, err := windowFormatsReadSegmentRaw(s.socket, topo.windows)
			if err != nil {
				// Unknown: keep writing frames into the segments, which
				// every format can show.
				logf("%v", err)
				raw = true
			}
			s.windowFormatsRaw, s.windowFormatsKey, s.windowFormatsAt = raw, key, time.Now()
		}
	}
	segCfg.frameRef = segmentReadersExpand(globals) && !s.windowFormatsRaw
	topo, activeWindows := s.topo, s.activeWindows
	pace := paceFor(globals, s.clients, s.focused)
	paneToWindow, panes, windows, seenAtByWindow := topo.paneToWindow, topo.panes, topo.windows, topo.seenAtByWindow

	state, nextSeenAtByWindow := buildFlashStateWithPaneIdentities(payload.Records, paneToWindow, windows, activeWindows, seenAtByWindow, nowUnix, flashGraceSeconds, s.socket, topo.paneIdentities)

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
	// Segments written by another daemon, or showing the frame the other way,
	// are rewritten.
	segmentsStale := !s.segmentsWritten || s.segmentsFrameRef != segCfg.frameRef
	visualChanged := nextStateHash != currentStateHash || nextFlashPhase != currentFlashPhase || segmentsStale
	spinnerChanged := spinnerFrame != globals["@ai_attn_spinner_frame"]
	if !visualChanged && !clearingStaleError && !spinnerChanged {
		return false, tickForState(state, tickMs, flashMultiplier, pace)
	}

	// A working spinner normally changes every tick. Keep that hot path small:
	// pane state, waiting counts, and non-working windows are unchanged, so only
	// publish the working window segments and the shared frame.
	if hasWorkingPane && spinnerChanged && !visualChanged && !clearingStaleError {
		batch := appendWorkingSpinnerUpdates(
			nil, windowStates, state.WindowsFlashing, nextFlashPhase, spinnerFrame, segCfg,
		)
		s.noteUpdate(s.publish(batch), queryErr)
		return false, tickForState(state, tickMs, flashMultiplier, pace)
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

	err := s.publish(batch)
	if err == nil {
		for windowID, nextSeenAt := range nextSeenAtByWindow {
			seenAtByWindow[windowID] = nextSeenAt
		}
		s.segmentsWritten, s.segmentsFrameRef = true, segCfg.frameRef
	}
	s.noteUpdate(err, queryErr)

	return false, tickForState(state, tickMs, flashMultiplier, pace)
}

// publish sends a sync's option updates, followed by a status redraw when
// @ai_attn_refresh_client is on, then brings the cached globals in line with
// what it wrote. A failed update leaves tmux's options unknown, so it drops
// the cache and the next sync reads tmux again.
func (s *syncer) publish(batch []string) error {
	refresh := isTruthy(strings.ToLower(s.globals["@ai_attn_refresh_client"]))
	if err := tmuxBatchRefresh(s.socket, batch, refresh); err != nil {
		s.globals, s.topo, s.activeWindows = nil, nil, nil
		return err
	}
	applyGlobalWrites(s.globals, batch)
	return nil
}

// applyGlobalWrites records the global options a batch set in globals.
func applyGlobalWrites(globals map[string]string, batch []string) {
	start := 0
	for i := 0; i <= len(batch); i++ {
		if i < len(batch) && batch[i] != ";" {
			continue
		}
		if cmd := batch[start:i]; len(cmd) == 4 && cmd[0] == "set-option" && cmd[1] == "-gq" {
			globals[cmd[2]] = cmd[3]
		}
		start = i + 1
	}
}

// paneTopology is what one list-panes read reports about panes and windows.
type paneTopology struct {
	paneToWindow   map[string]string
	paneIdentities map[string]paneIdentity
	panes          map[string]struct{}
	windows        map[string]struct{}
	seenAtByWindow map[string]int64
}

// parsePaneLines parses list-panes output in the format syncOnce requests:
// pane ID, window ID, @ai_attn_window_seen_at, command, and path, tab-separated.
func parsePaneLines(out string) paneTopology {
	topo := paneTopology{
		paneToWindow:   map[string]string{},
		paneIdentities: map[string]paneIdentity{},
		panes:          map[string]struct{}{},
		windows:        map[string]struct{}{},
		seenAtByWindow: map[string]int64{},
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 2 {
			continue
		}
		paneID := parts[0]
		windowID := parts[1]
		topo.paneToWindow[paneID] = windowID
		if len(parts) == 5 {
			topo.paneIdentities[paneID] = paneIdentity{Command: parts[3], CWD: parts[4]}
		}
		topo.panes[paneID] = struct{}{}
		if _, seen := topo.windows[windowID]; !seen {
			topo.windows[windowID] = struct{}{}
			seenAt := int64(0)
			if len(parts) >= 3 {
				if v, parseErr := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); parseErr == nil {
					seenAt = v
				}
			}
			topo.seenAtByWindow[windowID] = seenAt
		}
	}
	return topo
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
//
// pace slows the animation ticks (the spinner and flashes) to what the status
// line can show; the slower age updates are left as they are.
func tickForState(state flashState, tickMs int, flashMultiplier int, pace displayPace) time.Duration {
	tick := animationTick(state, tickMs, flashMultiplier)
	if tick == 0 || tick >= ageTick {
		return tick
	}
	if pace.paused {
		return pausedTick
	}
	return max(tick, pace.minTick)
}

// ageTick is how often panes with a state, but nothing animating, sync, so
// their ages stay current.
const ageTick = 10 * time.Second

// animationTick is the tick interval the state needs at full speed.
func animationTick(state flashState, tickMs int, flashMultiplier int) time.Duration {
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
		return ageTick
	}
	return 0
}

// parseClientLines reads list-clients output in the "#{window_id}\t#{client_flags}"
// format: the windows clients show, how many clients there are, and whether
// any has focus.
func parseClientLines(lines string) (activeWindows map[string]struct{}, clients int, focused bool) {
	activeWindows = map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(lines), "\n") {
		windowID, flags, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if windowID == "" {
			continue
		}
		clients++
		activeWindows[windowID] = struct{}{}
		for _, flag := range strings.Split(flags, ",") {
			if flag == "focused" {
				focused = true
			}
		}
	}
	return activeWindows, clients, focused
}

// buildFlashState computes the current attention/flash state for all panes and windows
// from the given records, topology, and timing information. Called from syncOnce.
func buildFlashState(records []record, paneToWindow map[string]string, windows map[string]struct{}, activeWindows map[string]struct{}, seenAtByWindow map[string]int64, nowUnix int64, flashGraceSeconds int64, daemonSocket string) (flashState, map[string]int64) {
	return buildFlashStateWithPaneIdentities(records, paneToWindow, windows, activeWindows, seenAtByWindow, nowUnix, flashGraceSeconds, daemonSocket, nil)
}

// buildFlashStateWithPaneIdentities also resolves records produced by detached
// agent runtimes. Those hooks have no tmux environment, so accept an exact
// agent/CWD match only when it identifies one pane on this server.
func buildFlashStateWithPaneIdentities(records []record, paneToWindow map[string]string, windows map[string]struct{}, activeWindows map[string]struct{}, seenAtByWindow map[string]int64, nowUnix int64, flashGraceSeconds int64, daemonSocket string, paneIdentities map[string]paneIdentity) (flashState, map[string]int64) {
	// For each target pane, keep the most recent record with a non-empty
	// state. This ensures a newer state from any source overrides an older one.
	// terminal-bell was produced by a retired Codex fallback; ignore retained
	// records during upgrade because a bell can follow a true Stop event.
	latestByPane := map[string]record{}
	for _, rec := range records {
		if rec.State == "" {
			continue
		}
		if rec.Reason == "terminal-bell" {
			continue
		}
		if rec.Socket != "" && daemonSocket != "" && rec.Socket != daemonSocket {
			continue
		}
		targetPaneID := rec.PaneID
		if targetPaneID == "" {
			targetPaneID = uniquePaneForUnscopedRecord(rec, paneIdentities)
			if targetPaneID == "" {
				continue
			}
			rec.PaneID = targetPaneID
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

func uniquePaneForUnscopedRecord(rec record, panes map[string]paneIdentity) string {
	if rec.PaneID != "" || rec.Socket != "" || rec.Agent == "" || rec.Agent == "pmux" || rec.CWD == "" {
		return ""
	}

	match := ""
	for paneID, pane := range panes {
		if pane.CWD != rec.CWD || !paneCommandMatchesAgent(pane.Command, rec.Agent) {
			continue
		}
		if match != "" {
			return ""
		}
		match = paneID
	}
	return match
}

func paneCommandMatchesAgent(command, agent string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if separator := strings.IndexAny(command, " \t"); separator >= 0 {
		command = command[:separator]
	}
	command = filepath.Base(command)
	if len(command) < len(agent) || !strings.EqualFold(command[:len(agent)], agent) {
		return false
	}
	return len(command) == len(agent) || !isASCIIAlphanumeric(command[len(agent)])
}

func isASCIIAlphanumeric(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
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

// tmux runs a tmux command against the given socket. Tests replace it to
// observe the commands a sync sends.
var tmux = execTmux

// execTmux runs a tmux command against the given socket with a 5-second timeout.
// Used by all other tmux helper functions (tmuxBatch, tmuxSetGlobal, etc.).
func execTmux(socket string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("tmux %s: timed out after 5s", describeArgs(args))
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("tmux %s: %s", describeArgs(args), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// tmuxBatch runs multiple tmux commands in a single invocation using ";"
// separators. The args slice should contain command tokens separated by ";"
// elements, e.g.: ["set-option", "-gq", "@foo", "bar", ";", "set-option", "-gq", "@baz", "qux"]
func tmuxBatch(socket string, args []string) error {
	for _, chunk := range splitBatch(args) {
		if _, err := tmux(socket, chunk...); err != nil {
			return err
		}
	}
	return nil
}

// tmuxRead runs args like tmuxBatch and returns what they print.
func tmuxRead(socket string, args []string) (string, error) {
	var out strings.Builder
	for _, chunk := range splitBatch(args) {
		printed, err := tmux(socket, chunk...)
		if err != nil {
			return "", err
		}
		out.WriteString(printed)
	}
	return out.String(), nil
}

// splitBatch splits a ";"-separated command list into invocations tmux will
// accept. tmux rejects a command string longer than its internal limit with
// "command too long" and discards the ENTIRE batch, so every option in it is
// silently lost. Splitting on ";" boundaries keeps each invocation small.
func splitBatch(args []string) [][]string {
	const maxBatchBytes = 4000

	var (
		chunks    [][]string
		chunk     []string
		chunkSize int
		cmd       []string
		cmdSize   int
	)
	flush := func() {
		for len(chunk) > 0 && chunk[len(chunk)-1] == ";" {
			chunk = chunk[:len(chunk)-1]
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
		chunk, chunkSize = nil, 0
	}
	appendCmd := func() {
		if len(cmd) == 0 {
			return
		}
		if chunkSize > 0 && chunkSize+cmdSize > maxBatchBytes {
			flush()
		}
		chunk = append(chunk, cmd...)
		chunkSize += cmdSize
		cmd, cmdSize = nil, 0
	}
	for _, a := range args {
		cmd = append(cmd, a)
		cmdSize += len(a) + 1
		if a == ";" {
			appendCmd()
		}
	}
	appendCmd()
	flush()
	return chunks
}

// tmuxBatchRefresh runs args like tmuxBatch and, when refresh is set, ends the
// final invocation with "refresh-client -S" to redraw status lines without
// spawning another client. tmux stops a command sequence at its first failure,
// so the refresh goes last, and its "no current client" error (no client is
// attached) is ignored.
func tmuxBatchRefresh(socket string, args []string, refresh bool) error {
	if refresh {
		if len(args) > 0 {
			args = append(args[:len(args):len(args)], ";")
		}
		args = append(args, "refresh-client", "-S")
	}
	err := tmuxBatch(socket, args)
	if err != nil && refresh && strings.Contains(err.Error(), "no current client") {
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

// appendWorkingSpinnerUpdates builds the minimal batch needed for a spinner
// frame change. Sorting keeps command order deterministic for tests and logs.
func appendWorkingSpinnerUpdates(batch []string, windowStates map[string]string, windowsFlashing map[string]bool, flashPhase, spinnerFrame string, cfg segmentConfig) []string {
	// Segments that refer to the frame pick up the new one from the global.
	if cfg.frameRef {
		return appendGlobalOption(batch, "@ai_attn_spinner_frame", spinnerFrame)
	}
	windowIDs := make([]string, 0, len(windowStates))
	for windowID, state := range windowStates {
		if state == "working" {
			windowIDs = append(windowIDs, windowID)
		}
	}
	sort.Strings(windowIDs)
	for _, windowID := range windowIDs {
		segment := renderWindowSegment("working", windowsFlashing[windowID], flashPhase, spinnerFrame, cfg)
		batch = appendWindowOption(batch, windowID, "@ai_attn_window_segment", segment)
	}
	return appendGlobalOption(batch, "@ai_attn_spinner_frame", spinnerFrame)
}

// tmuxSetGlobal sets a single global tmux option. Called from syncOnce to record errors.
func tmuxSetGlobal(socket, name, value string) error {
	_, err := tmux(socket, "set-option", "-gq", name, value)
	return err
}

// tmuxGetGlobalOptions fetches all global user options matching the given prefix
// in a single tmux call and returns them as a map of name->value.
//
// The global session, server, and window options are read in one call, and
// besides the prefixed options the result has those paceFor needs and the
// formats segmentReadersExpand checks.
func tmuxGetGlobalOptions(socket, prefix string) (map[string]string, error) {
	out, err := tmux(socket, "show-options", "-gq", ";", "show-options", "-sq", ";", "show-options", "-gwq")
	if err != nil {
		return nil, err
	}
	options := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, _ := strings.Cut(line, " ")
		if !strings.HasPrefix(name, prefix) && name != "status-interval" && name != "focus-events" && !isSegmentReaderOption(name) {
			continue
		}
		value = strings.TrimPrefix(strings.TrimSuffix(value, "\""), "\"")
		options[name] = value
	}
	return options, nil
}

// windowFormatOptions are the window options whose formats may show
// @ai_attn_window_segment.
var windowFormatOptions = []string{"window-status-format", "window-status-current-format", "pane-border-format"}

// isSegmentReaderOption reports whether the option named name holds a format
// that may show @ai_attn_window_segment: a window format, or a status line.
func isSegmentReaderOption(name string) bool {
	return slices.Contains(windowFormatOptions, name) ||
		name == "status-left" || name == "status-right" || strings.HasPrefix(name, "status-format[")
}

// segmentReadersExpand reports whether every global format that shows
// @ai_attn_window_segment expands it (#{E:...}), so a segment may refer to
// the spinner frame instead of containing it.
func segmentReadersExpand(globals map[string]string) bool {
	for name, value := range globals {
		if isSegmentReaderOption(name) && readsSegmentRaw(value) {
			return false
		}
	}
	return true
}

// readsSegmentRaw reports whether format reads @ai_attn_window_segment
// without expanding it, which would show a frame reference as text. A read
// expands when its modifiers include E or T; a conditional only tests it.
func readsSegmentRaw(format string) bool {
	const name = "@ai_attn_window_segment"
	rest := format
	for {
		i := strings.Index(rest, name)
		if i < 0 {
			return false
		}
		open := strings.LastIndex(rest[:i], "#{")
		if open < 0 {
			return true
		}
		modifiers := rest[open+2 : i]
		expands := strings.HasPrefix(modifiers, "?")
		for _, modifier := range strings.Split(strings.TrimSuffix(modifiers, ":"), ";") {
			if modifier == "E" || modifier == "T" {
				expands = true
			}
		}
		if !expands {
			return true
		}
		rest = rest[i+len(name):]
	}
}

// windowsKey identifies a set of windows.
func windowsKey(windows map[string]struct{}) string {
	ids := make([]string, 0, len(windows))
	for id := range windows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// windowFormatsReadSegmentRaw reports whether a format set on one of windows
// (rather than globally) reads @ai_attn_window_segment without expanding it.
// show-options -w with a target and name prints only an option set on that
// window, so one call asks every window and normally prints nothing.
func windowFormatsReadSegmentRaw(socket string, windows map[string]struct{}) (bool, error) {
	var args []string
	for _, id := range strings.Split(windowsKey(windows), ",") {
		if id == "" {
			continue
		}
		for _, name := range windowFormatOptions {
			args = append(args, "show-options", "-wq", "-t", id, name, ";")
		}
	}
	out, err := tmuxRead(socket, args)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		name, value, _ := strings.Cut(line, " ")
		if slices.Contains(windowFormatOptions, name) && readsSegmentRaw(value) {
			return true, nil
		}
	}
	return false, nil
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
		if isConfigOption(name) {
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

func isConfigOption(name string) bool {
	switch name {
	case "@ai_attn_cli", "@ai_attn_plugin_dir",
		"@ai_attn_seen_flash_seconds", "@ai_attn_refresh_client",
		"@ai_attn_dev_build", "@ai_attn_enable_default_formats",
		"@ai_attn_icon_waiting", "@ai_attn_icon_stopped", "@ai_attn_icon_done",
		"@ai_attn_color_waiting", "@ai_attn_color_stopped", "@ai_attn_color_working",
		"@ai_attn_color_done", "@ai_attn_color_flash_bg", "@ai_attn_color_flash_fg",
		"@ai_attn_color_text_fg", "@ai_attn_spinner_frames",
		"@ai_attn_tick_ms", "@ai_attn_flash_multiplier":
		return true
	default:
		return false
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
