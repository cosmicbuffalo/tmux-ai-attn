package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBuildFlashStateKeepsCurrentWindowFlashingDuringGrace verifies that the active window
// continues flashing while a waiting record is still within the grace period.
func TestBuildFlashStateKeepsCurrentWindowFlashingDuringGrace(t *testing.T) {
	records := []record{{
		Agent:     "codex",
		PaneID:    "%1",
		Reason:    "agent-turn-complete",
		UpdatedAt: 100,
		State:     "waiting",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if !state.WindowsFlashing["@1"] {
		t.Fatal("expected current window to keep flashing during grace period")
	}
	if !state.PanesFlashing["%1"] {
		t.Fatal("expected pane flash while record is within grace period")
	}
	if nextSeenAt["@1"] != 0 {
		t.Fatalf("expected seen_at to remain unchanged during grace period, got %d", nextSeenAt["@1"])
	}
}

// TestBuildFlashStateMarksCurrentWindowSeenAfterGrace verifies that the active window
// stops flashing and is marked as seen once the grace period expires.
func TestBuildFlashStateMarksCurrentWindowSeenAfterGrace(t *testing.T) {
	records := []record{{
		Agent:     "codex",
		PaneID:    "%1",
		Reason:    "agent-turn-complete",
		UpdatedAt: 100,
		State:     "waiting",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 103, 3, "")

	if state.WindowsFlashing["@1"] {
		t.Fatal("expected current window flash to stop after grace period")
	}
	if state.PanesFlashing["%1"] {
		t.Fatal("expected pane flash to stop after grace period")
	}
	if nextSeenAt["@1"] != 100 {
		t.Fatalf("expected seen_at to be updated to latest waiting timestamp, got %d", nextSeenAt["@1"])
	}
}

// TestBuildFlashStateKeepsBackgroundWindowFlashingUntilSeen verifies that a background
// (non-active) window keeps flashing until the user switches to it.
func TestBuildFlashStateKeepsBackgroundWindowFlashingUntilSeen(t *testing.T) {
	records := []record{{
		Agent:     "codex",
		PaneID:    "%2",
		Reason:    "permission_required",
		UpdatedAt: 100,
		State:     "waiting",
	}}
	paneToWindow := map[string]string{"%2": "@2"}
	windows := map[string]struct{}{"@2": {}}
	activeWindows := map[string]struct{}{}
	seenAt := map[string]int64{"@2": 0}

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 110, 3, "")

	if !state.WindowsFlashing["@2"] {
		t.Fatal("expected background window to keep flashing until explicitly seen")
	}
	if state.PanesFlashing["%2"] {
		t.Fatal("expected pane flash to expire after grace period even while window still flashes")
	}
	if nextSeenAt["@2"] != 0 {
		t.Fatalf("expected seen_at to remain unchanged for background window, got %d", nextSeenAt["@2"])
	}
}

// TestBuildFlashStateEmptyRecords verifies that no flash or waiting state is produced
// when there are no attention records.
func TestBuildFlashStateEmptyRecords(t *testing.T) {
	paneToWindow := map[string]string{"%1": "@1", "%2": "@2"}
	windows := map[string]struct{}{"@1": {}, "@2": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0, "@2": 0}

	state, nextSeenAt := buildFlashState(nil, paneToWindow, windows, activeWindows, seenAt, 200, 3, "")

	if len(state.WaitingPanes) != 0 {
		t.Fatalf("expected no waiting panes, got %d", len(state.WaitingPanes))
	}
	if len(state.WaitingWindows) != 0 {
		t.Fatalf("expected no waiting windows, got %d", len(state.WaitingWindows))
	}
	for wid, flash := range state.WindowsFlashing {
		if flash {
			t.Fatalf("expected window %s flash=false with no records", wid)
		}
	}
	for wid, sa := range nextSeenAt {
		if sa != 0 {
			t.Fatalf("expected seen_at=0 for window %s with no records, got %d", wid, sa)
		}
	}
}

// TestBuildFlashStateMultipleRecordsSamePane verifies that when multiple records target
// the same pane, only the most recent one is used (deduplication).
func TestBuildFlashStateMultipleRecordsSamePane(t *testing.T) {
	records := []record{
		{
			Agent:     "codex",
			PaneID:    "%1",
			Reason:    "permission_required",
			UpdatedAt: 100,
			State:     "waiting",
		},
		{
			Agent:     "claude",
			PaneID:    "%1",
			Reason:    "agent-turn-complete",
			UpdatedAt: 105,
			State:     "waiting",
		},
	}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 106, 3, "")

	if state.WaitingWindows["@1"] != 1 {
		t.Fatalf("expected window waiting count=1 for deduplicated pane, got %d", state.WaitingWindows["@1"])
	}
	if _, ok := state.WaitingPanes["%1"]; !ok {
		t.Fatal("expected pane %1 to be in waiting panes")
	}
}

// TestStateHashDiffersOnFlashChange verifies that the state hash changes when
// flash state differs, and is stable for identical inputs.
func TestStateHashDiffersOnFlashChange(t *testing.T) {
	activePanes := map[string]record{
		"%2": {State: "waiting", Agent: "codex", Reason: "permission_required"},
	}
	waitingPanes := map[string]record{
		"%2": {State: "waiting", Agent: "codex", Reason: "permission_required"},
	}
	waitingWindows := map[string]int{"@2": 1}

	h1 := stateHash(activePanes, waitingPanes, waitingWindows, map[string]bool{"@2": true}, map[string]bool{"%2": false})
	h2 := stateHash(activePanes, waitingPanes, waitingWindows, map[string]bool{"@2": false}, map[string]bool{"%2": false})

	if h1 == h2 {
		t.Fatal("expected different hashes when flash state changes")
	}

	// Same inputs should produce the same hash.
	h3 := stateHash(activePanes, waitingPanes, waitingWindows, map[string]bool{"@2": true}, map[string]bool{"%2": false})
	if h1 != h3 {
		t.Fatal("expected identical hashes for identical inputs")
	}
}

// TestStateHashEmptyIsStable verifies that nil and empty maps produce the same hash,
// and that non-empty state produces a different hash.
func TestStateHashEmptyIsStable(t *testing.T) {
	h1 := stateHash(nil, nil, nil, nil, nil)
	h2 := stateHash(map[string]record{}, map[string]record{}, map[string]int{}, map[string]bool{}, map[string]bool{})

	if h1 != h2 {
		t.Fatal("expected empty maps and nil maps to produce the same hash")
	}

	// Non-empty must differ from empty.
	h3 := stateHash(map[string]record{"%1": {Agent: "codex"}}, map[string]record{"%1": {Agent: "codex"}}, map[string]int{"@1": 1}, nil, nil)
	if h1 == h3 {
		t.Fatal("expected non-empty hash to differ from empty hash")
	}
}

// TestTickForStateFlashingWithWorkingReturns120ms verifies that tickForState returns 120ms
// when both working and flashing panes exist (working's 120ms is faster than flash's 480ms).
func TestTickForStateFlashingWithWorkingReturns120ms(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "working", Agent: "claude"},
			"%2": {State: "waiting", Agent: "codex"},
		},
		PanesFlashing: map[string]bool{
			"%2": true,
		},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 120*time.Millisecond {
		t.Fatalf("expected 120ms tick when working+flashing, got %v", got)
	}
}

// TestTickForStateWorkingOnlyReturns120ms verifies that tickForState returns 120ms when
// a working pane exists (for spinner animation) even without flashing.
func TestTickForStateWorkingOnlyReturns120ms(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "working", Agent: "claude"},
		},
		PanesFlashing: map[string]bool{},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 120*time.Millisecond {
		t.Fatalf("expected 120ms tick for working state (spinner), got %v", got)
	}
}

// TestTickForStateWaitingReturns10s verifies that tickForState returns 10s for
// waiting (non-flashing) panes to update age text.
func TestTickForStateWaitingReturns10s(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "waiting", Agent: "claude"},
		},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 10*time.Second {
		t.Fatalf("expected 10s tick for waiting state, got %v", got)
	}
}

// TestTickForStateDoneReturns10s verifies that tickForState returns 10s for panes
// in "done" state to update age text.
func TestTickForStateDoneReturns10s(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "done", Agent: "claude"},
		},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 10*time.Second {
		t.Fatalf("expected 10s tick for done state, got %v", got)
	}
}

// TestTickForStateIdleReturnsZero verifies that tickForState returns 0 (no ticking needed)
// when there are no active panes.
func TestTickForStateIdleReturnsZero(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 0 {
		t.Fatalf("expected 0 tick for idle state, got %v", got)
	}
}

func TestLoopTickIntervalKeepsIdleServerHealthCheck(t *testing.T) {
	if got := loopTickInterval(0); got != idleServerCheckInterval {
		t.Fatalf("loopTickInterval(0) = %v, want %v", got, idleServerCheckInterval)
	}
	want := 120 * time.Millisecond
	if got := loopTickInterval(want); got != want {
		t.Fatalf("loopTickInterval(%v) = %v, want unchanged", want, got)
	}
}

func TestTmuxServerIsGoneRecognizesStaleSocketErrors(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "tmux.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		"no server running on " + socket,
		"failed to connect to server: Connection refused",
		"server exited unexpectedly",
	} {
		if !tmuxServerIsGone(socket, errors.New(message)) {
			t.Fatalf("expected server-gone classification for %q", message)
		}
	}
	if tmuxServerIsGone(socket, errors.New("temporary tmux error")) {
		t.Fatal("generic error with an existing socket should remain retryable")
	}
}

func TestTmuxServerIsGoneRecognizesMissingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	if !tmuxServerIsGone(socket, errors.New("generic tmux error")) {
		t.Fatal("missing socket should mean the server is gone")
	}
}

// ---------------------------------------------------------------------------
// buildFlashState edge cases
// ---------------------------------------------------------------------------

// TestBuildFlashStateWorkingPaneNotInWaiting verifies that a record with State="working"
// appears in StatefulPanes but NOT in WaitingPanes or WaitingWindows.
func TestBuildFlashStateWorkingPaneNotInWaiting(t *testing.T) {
	records := []record{{
		Agent:     "claude",
		PaneID:    "%1",
		Reason:    "tool-use",
		UpdatedAt: 100,
		State:     "working",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if _, ok := state.StatefulPanes["%1"]; !ok {
		t.Fatal("expected working pane in StatefulPanes")
	}
	if _, ok := state.WaitingPanes["%1"]; ok {
		t.Fatal("expected working pane NOT in WaitingPanes")
	}
	if state.WaitingWindows["@1"] != 0 {
		t.Fatalf("expected WaitingWindows count=0 for working pane, got %d", state.WaitingWindows["@1"])
	}
}

// TestBuildFlashStateUnknownPaneDropped verifies that a record with a PaneID not
// present in paneToWindow is silently dropped.
func TestBuildFlashStateUnknownPaneDropped(t *testing.T) {
	records := []record{{
		Agent:     "codex",
		PaneID:    "%99",
		Reason:    "permission_required",
		UpdatedAt: 100,
		State:     "waiting",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected no stateful panes for unknown pane ID, got %d", len(state.StatefulPanes))
	}
	if len(state.WaitingPanes) != 0 {
		t.Fatalf("expected no waiting panes for unknown pane ID, got %d", len(state.WaitingPanes))
	}
}

// TestBuildFlashStateMixedStatesInWindow verifies that two panes in the same window
// with different states produce correct WaitingWindows count and StatefulPanes.
func TestBuildFlashStateMixedStatesInWindow(t *testing.T) {
	records := []record{
		{
			Agent:     "claude",
			PaneID:    "%1",
			Reason:    "tool-use",
			UpdatedAt: 100,
			State:     "working",
		},
		{
			Agent:     "codex",
			PaneID:    "%2",
			Reason:    "permission_required",
			UpdatedAt: 100,
			State:     "waiting",
		},
	}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if state.WaitingWindows["@1"] != 1 {
		t.Fatalf("expected WaitingWindows count=1 (only waiting pane), got %d", state.WaitingWindows["@1"])
	}
	if len(state.StatefulPanes) != 2 {
		t.Fatalf("expected 2 stateful panes, got %d", len(state.StatefulPanes))
	}
	if _, ok := state.StatefulPanes["%1"]; !ok {
		t.Fatal("expected working pane %1 in StatefulPanes")
	}
	if _, ok := state.StatefulPanes["%2"]; !ok {
		t.Fatal("expected waiting pane %2 in StatefulPanes")
	}
}

// TestBuildFlashStateNewerWorkingOverridesWaiting verifies that when two records
// exist for the same pane, the newer working record overrides the older waiting one.
func TestBuildFlashStateNewerWorkingOverridesWaiting(t *testing.T) {
	records := []record{
		{
			Agent:     "codex",
			PaneID:    "%1",
			Reason:    "permission_required",
			UpdatedAt: 100,
			State:     "waiting",
		},
		{
			Agent:     "codex",
			PaneID:    "%1",
			Reason:    "tool-use",
			UpdatedAt: 105,
			State:     "working",
		},
	}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 106, 3, "")

	if _, ok := state.StatefulPanes["%1"]; !ok {
		t.Fatal("expected pane %1 in StatefulPanes")
	}
	if state.StatefulPanes["%1"].State != "working" {
		t.Fatalf("expected StatefulPanes state=working, got %s", state.StatefulPanes["%1"].State)
	}
	if _, ok := state.WaitingPanes["%1"]; ok {
		t.Fatal("expected pane %1 NOT in WaitingPanes after newer working override")
	}
}

// TestBuildFlashStateEmptyPaneIDDropped verifies that a record with PaneID="" is dropped.
func TestBuildFlashStateEmptyPaneIDDropped(t *testing.T) {
	records := []record{{
		Agent:     "codex",
		PaneID:    "",
		Reason:    "permission_required",
		UpdatedAt: 100,
		State:     "waiting",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected no stateful panes for empty PaneID, got %d", len(state.StatefulPanes))
	}
	if len(state.WaitingPanes) != 0 {
		t.Fatalf("expected no waiting panes for empty PaneID, got %d", len(state.WaitingPanes))
	}
}

func TestBuildFlashStateResolvesUniqueUnscopedAgentCWD(t *testing.T) {
	records := []record{{
		Agent:     "claude",
		CWD:       "/work/tweaker",
		Reason:    "PostToolUse",
		UpdatedAt: 100,
		State:     "working",
	}}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@2"}
	windows := map[string]struct{}{"@1": {}, "@2": {}}
	panes := map[string]paneIdentity{
		"%1": {Command: "claude", CWD: "/work/tweaker"},
		"%2": {Command: "codex", CWD: "/work/tweaker"},
	}

	state, _ := buildFlashStateWithPaneIdentities(
		records, paneToWindow, windows, nil, nil, 102, 3, "/tmp/tmux/pmux", panes,
	)

	if got := state.StatefulPanes["%1"].State; got != "working" {
		t.Fatalf("expected unique Claude pane to resolve as working, got %q", got)
	}
	if _, ok := state.StatefulPanes["%2"]; ok {
		t.Fatal("expected Codex pane not to match Claude record")
	}
}

func TestBuildFlashStateRejectsAmbiguousUnscopedAgentCWD(t *testing.T) {
	records := []record{{
		Agent:     "claude",
		CWD:       "/work/shared",
		UpdatedAt: 100,
		State:     "working",
	}}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@2"}
	windows := map[string]struct{}{"@1": {}, "@2": {}}
	panes := map[string]paneIdentity{
		"%1": {Command: "claude", CWD: "/work/shared"},
		"%2": {Command: "claude-2", CWD: "/work/shared"},
	}

	state, _ := buildFlashStateWithPaneIdentities(
		records, paneToWindow, windows, nil, nil, 102, 3, "/tmp/tmux/pmux", panes,
	)

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected ambiguous unscoped record to be dropped, got %v", state.StatefulPanes)
	}
}

func TestBuildFlashStateDoesNotRemapScopedRecordWithoutPane(t *testing.T) {
	records := []record{{
		Agent:     "claude",
		Socket:    "/tmp/tmux/pmux",
		CWD:       "/work/tweaker",
		UpdatedAt: 100,
		State:     "working",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	panes := map[string]paneIdentity{
		"%1": {Command: "claude", CWD: "/work/tweaker"},
	}

	state, _ := buildFlashStateWithPaneIdentities(
		records, paneToWindow, windows, nil, nil, 102, 3, "/tmp/tmux/pmux", panes,
	)

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected malformed scoped record to remain unmatched, got %v", state.StatefulPanes)
	}
}

// ---------------------------------------------------------------------------
// Pure function tests
// ---------------------------------------------------------------------------

// TestBoolString verifies boolString returns "1" for true and "0" for false.
func TestBoolString(t *testing.T) {
	if got := boolString(true); got != "1" {
		t.Fatalf("boolString(true) = %q, want %q", got, "1")
	}
	if got := boolString(false); got != "0" {
		t.Fatalf("boolString(false) = %q, want %q", got, "0")
	}
}

// TestIsTruthy verifies isTruthy with a table-driven approach (case-sensitive).
func TestIsTruthy(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"1", true},
		{"on", true},
		{"yes", true},
		{"true", true},
		{"0", false},
		{"off", false},
		{"", false},
		{"ON", false}, // case-sensitive: uppercase is NOT truthy
	}
	for _, tc := range cases {
		got := isTruthy(tc.input)
		if got != tc.want {
			t.Errorf("isTruthy(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestAppendGlobalOption verifies the batch structure for appendGlobalOption.
func TestAppendGlobalOption(t *testing.T) {
	// On empty batch: no separator.
	batch := appendGlobalOption(nil, "@foo", "bar")
	joined := strings.Join(batch, " ")
	if strings.Contains(joined, ";") {
		t.Fatal("expected no separator on empty batch")
	}
	if !contains(batch, "set-option") || !contains(batch, "-gq") || !contains(batch, "@foo") || !contains(batch, "bar") {
		t.Fatalf("unexpected batch contents: %v", batch)
	}

	// On non-empty batch: ";" separator prepended.
	batch = appendGlobalOption(batch, "@baz", "qux")
	if batch[4] != ";" {
		t.Fatalf("expected separator at index 4, got %q", batch[4])
	}
	if !contains(batch, "@baz") || !contains(batch, "qux") {
		t.Fatalf("unexpected batch contents: %v", batch)
	}
}

// TestAppendWindowOption verifies the batch structure for appendWindowOption.
func TestAppendWindowOption(t *testing.T) {
	batch := appendWindowOption(nil, "@1", "@name", "val")
	if !contains(batch, "set-window-option") || !contains(batch, "-t") || !contains(batch, "@1") || !contains(batch, "-q") || !contains(batch, "@name") || !contains(batch, "val") {
		t.Fatalf("unexpected batch contents: %v", batch)
	}
}

// TestAppendPaneOption verifies the batch structure for appendPaneOption.
func TestAppendPaneOption(t *testing.T) {
	batch := appendPaneOption(nil, "%1", "@name", "val")
	if !contains(batch, "set-option") || !contains(batch, "-pt") || !contains(batch, "%1") || !contains(batch, "-q") || !contains(batch, "@name") || !contains(batch, "val") {
		t.Fatalf("unexpected batch contents: %v", batch)
	}
}

// TestAppendPaneStateActive verifies appendPaneState with an active (non-empty) state
// produces 5 set-option commands with correct values.
func TestAppendPaneStateActive(t *testing.T) {
	batch := appendPaneState(nil, "%5", "waiting", "codex", "permission_required", 12345, true)

	// Should contain 5 set-option commands (state, agent, reason, updated_at, flash).
	count := 0
	for _, arg := range batch {
		if arg == "set-option" {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("expected 5 set-option commands, got %d", count)
	}

	if !contains(batch, "waiting") {
		t.Fatal("expected state value 'waiting' in batch")
	}
	if !contains(batch, "codex") {
		t.Fatal("expected agent value 'codex' in batch")
	}
	if !contains(batch, "permission_required") {
		t.Fatal("expected reason value 'permission_required' in batch")
	}
	if !contains(batch, strconv.FormatInt(12345, 10)) {
		t.Fatal("expected updated_at value '12345' in batch")
	}
	if !contains(batch, "1") {
		t.Fatal("expected flash value '1' in batch")
	}
}

// TestAppendPaneStateCleared verifies appendPaneState with empty/zero values
// produces commands with empty strings and "0" values.
func TestAppendPaneStateCleared(t *testing.T) {
	batch := appendPaneState(nil, "%5", "", "", "", 0, false)

	count := 0
	for _, arg := range batch {
		if arg == "set-option" {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("expected 5 set-option commands, got %d", count)
	}

	// All values should be empty or "0".
	// The batch should NOT contain any non-empty state/agent/reason values.
	for _, arg := range batch {
		if arg == "waiting" || arg == "working" || arg == "codex" || arg == "claude" {
			t.Fatalf("unexpected non-empty value in cleared batch: %q", arg)
		}
	}
	// Flash should be "0".
	if !contains(batch, "0") {
		t.Fatal("expected '0' for flash in cleared batch")
	}
}

// TestAppendWorkingSpinnerUpdatesOnlyTouchesAnimatedFields verifies the hot
// working-state tick does not rewrite stable pane/window attention options.
func TestAppendWorkingSpinnerUpdatesOnlyTouchesAnimatedFields(t *testing.T) {
	batch := appendWorkingSpinnerUpdates(
		nil,
		map[string]string{"@2": "working", "@1": "working", "@3": "waiting"},
		map[string]bool{"@1": false, "@2": false, "@3": true},
		"0",
		"✻",
		defaultSegmentConfig(),
	)

	windowUpdates := 0
	globalUpdates := 0
	for _, arg := range batch {
		switch arg {
		case "set-window-option":
			windowUpdates++
		case "set-option":
			globalUpdates++
		}
	}
	if windowUpdates != 2 {
		t.Fatalf("expected 2 working-window updates, got %d: %v", windowUpdates, batch)
	}
	if globalUpdates != 1 {
		t.Fatalf("expected 1 spinner-frame update, got %d: %v", globalUpdates, batch)
	}
	for _, stableOption := range []string{
		"@ai_attn_window_state",
		"@ai_attn_window_waiting",
		"@ai_attn_pane_state",
		"@ai_attn_state_hash",
	} {
		if contains(batch, stableOption) {
			t.Fatalf("spinner-only batch unexpectedly rewrites %s: %v", stableOption, batch)
		}
	}
	if !contains(batch, "@ai_attn_spinner_frame") || !contains(batch, "✻") {
		t.Fatalf("spinner-only batch is missing the current frame: %v", batch)
	}
	joined := strings.Join(batch, "\x00")
	if strings.Index(joined, "@1") > strings.Index(joined, "@2") {
		t.Fatalf("working windows should be emitted in stable order: %v", batch)
	}
}

func TestConfigOptionClassificationPreservesPluginOverrides(t *testing.T) {
	for _, name := range []string{
		"@ai_attn_plugin_dir",
		"@ai_attn_dev_build",
		"@ai_attn_refresh_client",
	} {
		if !isConfigOption(name) {
			t.Fatalf("expected %s to survive daemon restart", name)
		}
	}
	if isConfigOption("@ai_attn_spinner_frame") {
		t.Fatal("runtime spinner frame should be cleared on daemon restart")
	}
}

// ---------------------------------------------------------------------------
// stateHash edge cases
// ---------------------------------------------------------------------------

// TestStateHashWorkingVsWaiting verifies that the same pane with State="waiting"
// vs State="working" produces different hashes.
func TestStateHashWorkingVsWaiting(t *testing.T) {
	h1 := stateHash(
		map[string]record{"%1": {State: "waiting", Agent: "codex"}},
		map[string]record{"%1": {Agent: "codex"}},
		map[string]int{"@1": 1},
		nil, nil,
	)
	h2 := stateHash(
		map[string]record{"%1": {State: "working", Agent: "codex"}},
		nil, nil, nil, nil,
	)
	if h1 == h2 {
		t.Fatal("expected different hashes for waiting vs working state")
	}
}

// TestStateHashDifferentAgentsSamePanes verifies that swapping agents between
// panes produces different hashes.
func TestStateHashDifferentAgentsSamePanes(t *testing.T) {
	h1 := stateHash(
		map[string]record{
			"%1": {State: "waiting", Agent: "codex"},
			"%2": {State: "waiting", Agent: "claude"},
		},
		map[string]record{
			"%1": {Agent: "codex"},
			"%2": {Agent: "claude"},
		},
		map[string]int{"@1": 2},
		nil, nil,
	)
	h2 := stateHash(
		map[string]record{
			"%1": {State: "waiting", Agent: "claude"},
			"%2": {State: "waiting", Agent: "codex"},
		},
		map[string]record{
			"%1": {Agent: "claude"},
			"%2": {Agent: "codex"},
		},
		map[string]int{"@1": 2},
		nil, nil,
	)
	if h1 == h2 {
		t.Fatal("expected different hashes when agents are swapped between panes")
	}
}

// TestStateHashStableOnMultipleCalls verifies that calling stateHash twice with
// the same input produces identical results.
func TestStateHashStableOnMultipleCalls(t *testing.T) {
	activePanes := map[string]record{
		"%1": {State: "waiting", Agent: "codex"},
		"%2": {State: "working", Agent: "claude"},
		"%3": {State: "waiting", Agent: "codex"},
	}
	waitingPanes := map[string]record{
		"%1": {Agent: "codex"},
		"%3": {Agent: "codex"},
	}
	waitingWindows := map[string]int{"@1": 1, "@2": 1}
	windowFlash := map[string]bool{"@1": true, "@2": false}
	paneFlash := map[string]bool{"%1": true, "%3": false}

	h1 := stateHash(activePanes, waitingPanes, waitingWindows, windowFlash, paneFlash)
	h2 := stateHash(activePanes, waitingPanes, waitingWindows, windowFlash, paneFlash)

	if h1 != h2 {
		t.Fatal("expected identical hashes on repeated calls with same input")
	}
}

// ---------------------------------------------------------------------------
// tickForState edge case
// ---------------------------------------------------------------------------

// TestTickForStateWindowFlashOnly verifies that tickForState returns 480ms when
// a window is flashing but no panes are flashing and no pane is working.
func TestTickForStateWindowFlashOnly(t *testing.T) {
	state := flashState{
		StatefulPanes:   map[string]record{"%1": {State: "waiting", Agent: "codex"}},
		PanesFlashing:   map[string]bool{},
		WindowsFlashing: map[string]bool{"@1": true},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 480*time.Millisecond {
		t.Fatalf("expected 480ms tick for window-only flash, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Spinner tests
// ---------------------------------------------------------------------------

// TestComputeSpinnerFrame verifies that computeSpinnerFrame returns a valid frame.
func TestComputeSpinnerFrame(t *testing.T) {
	frame := computeSpinnerFrame(defaultSpinnerFrames, 120)
	valid := map[string]bool{"·": true, "✢": true, "✳": true, "✶": true, "✽": true, "✻": true}
	if !valid[frame] {
		t.Fatalf("computeSpinnerFrame returned unexpected frame %q", frame)
	}
}

// TestComputeSpinnerFrameHonorsTickMs verifies that the divisor used to pick a
// frame index follows the configured tickMs so spinner cadence stays in step
// with the syncer tick when @ai_attn_tick_ms is customized.
func TestComputeSpinnerFrameHonorsTickMs(t *testing.T) {
	frames := []string{"a", "b", "c", "d"}
	// At tickMs=240 the frame index should change roughly half as often as at
	// tickMs=120. Sample a small window and confirm the configured divisor is
	// used by checking that the returned frame is still a member of the slice.
	for _, tickMs := range []int{60, 120, 240, 480} {
		got := computeSpinnerFrame(frames, tickMs)
		found := false
		for _, f := range frames {
			if got == f {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tickMs=%d: returned %q not in frames", tickMs, got)
		}
	}
	// tickMs <= 0 should not panic and should fall back to a safe default.
	if got := computeSpinnerFrame(frames, 0); got == "" {
		t.Fatalf("tickMs=0 returned empty frame")
	}
}

// TestParseSpinnerFramesDefault verifies default frames when option is unset.
func TestParseSpinnerFramesDefault(t *testing.T) {
	frames := parseSpinnerFrames(map[string]string{})
	if len(frames) != len(defaultSpinnerFrames) {
		t.Fatalf("expected %d default frames, got %d", len(defaultSpinnerFrames), len(frames))
	}
}

// TestParseSpinnerFramesCustom verifies custom comma-separated frames.
func TestParseSpinnerFramesCustom(t *testing.T) {
	frames := parseSpinnerFrames(map[string]string{"@ai_attn_spinner_frames": "A,B,C"})
	if len(frames) != 3 || frames[0] != "A" || frames[1] != "B" || frames[2] != "C" {
		t.Fatalf("expected [A B C], got %v", frames)
	}
}

// TestParseSpinnerFramesSingleIsAccepted verifies that a single-frame input is
// honored rather than silently replaced with the default frames.
func TestParseSpinnerFramesSingleIsAccepted(t *testing.T) {
	frames := parseSpinnerFrames(map[string]string{"@ai_attn_spinner_frames": "X"})
	if len(frames) != 1 || frames[0] != "X" {
		t.Fatalf("expected single-frame [X], got %v", frames)
	}
}

// TestParseSpinnerFramesFiltersEmpty verifies that empty entries (e.g. from
// a trailing comma or "," alone) are dropped, and that an input that contains
// only empty fields falls back to the default frame set rather than rendering
// blank spinner glyphs.
func TestParseSpinnerFramesFiltersEmpty(t *testing.T) {
	frames := parseSpinnerFrames(map[string]string{"@ai_attn_spinner_frames": "A,,B,"})
	if len(frames) != 2 || frames[0] != "A" || frames[1] != "B" {
		t.Fatalf("expected [A B] after filtering empty entries, got %v", frames)
	}
	frames = parseSpinnerFrames(map[string]string{"@ai_attn_spinner_frames": ","})
	if len(frames) != len(defaultSpinnerFrames) {
		t.Fatalf("expected default frames for empty-only input, got %v", frames)
	}
}

// TestAdvanceFlashPhaseResetsWhenIdle verifies that when no flash is needed,
// the per-tick counter resets to zero and the returned phase is "0" so that
// the next flash burst starts from a deterministic state.
func TestAdvanceFlashPhaseResetsWhenIdle(t *testing.T) {
	s := &syncer{flashTickCounter: 7, flashPhaseCounter: 3}
	got := s.advanceFlashPhase(false, false, 4)
	if got != "0" {
		t.Fatalf("expected idle phase \"0\", got %q", got)
	}
	if s.flashTickCounter != 0 {
		t.Fatalf("expected flashTickCounter to reset to 0, got %d", s.flashTickCounter)
	}
}

// TestAdvanceFlashPhaseTogglesAtFullCadence verifies that without a working
// pane the phase toggles every tick (no multiplier gating).
func TestAdvanceFlashPhaseTogglesAtFullCadence(t *testing.T) {
	s := &syncer{}
	got := []string{}
	for i := 0; i < 4; i++ {
		got = append(got, s.advanceFlashPhase(true, false, 4))
	}
	want := []string{"1", "0", "1", "0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tick %d: expected %q, got %q (full sequence %v)", i, want[i], got[i], got)
		}
	}
}

// TestAdvanceFlashPhaseGatedByMultiplierWhenWorking verifies that with a
// working pane (fast tick) the phase only flips every flashMultiplier ticks.
func TestAdvanceFlashPhaseGatedByMultiplierWhenWorking(t *testing.T) {
	s := &syncer{}
	got := []string{}
	for i := 0; i < 8; i++ {
		got = append(got, s.advanceFlashPhase(true, true, 4))
	}
	// flashPhaseCounter increments on tick 4 and tick 8 → phase "1" then "0".
	want := []string{"0", "0", "0", "1", "1", "1", "1", "0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tick %d: expected %q, got %q (full sequence %v)", i, want[i], got[i], got)
		}
	}
}

// TestTickForStateFlashOnlyReturns480ms verifies that tickForState returns 480ms
// when panes are flashing but none are working.
func TestTickForStateFlashOnlyReturns480ms(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "waiting", Agent: "codex"},
		},
		PanesFlashing: map[string]bool{"%1": true},
	}
	got := tickForState(state, 120, 4, displayPace{})
	if got != 480*time.Millisecond {
		t.Fatalf("expected 480ms tick for flash-only, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Stopped state tests
// ---------------------------------------------------------------------------

// TestBuildFlashStateStoppedPaneNotInWaiting verifies that a record with State="stopped"
// appears in StatefulPanes but NOT in WaitingPanes or WaitingWindows.
func TestBuildFlashStateStoppedPaneNotInWaiting(t *testing.T) {
	records := []record{{
		Agent:     "claude",
		PaneID:    "%1",
		Reason:    "StopFailure",
		UpdatedAt: 100,
		State:     "stopped",
	}}
	paneToWindow := map[string]string{"%1": "@1"}
	windows := map[string]struct{}{"@1": {}}
	activeWindows := map[string]struct{}{"@1": {}}
	seenAt := map[string]int64{"@1": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if _, ok := state.StatefulPanes["%1"]; !ok {
		t.Fatal("expected stopped pane in StatefulPanes")
	}
	if state.StatefulPanes["%1"].State != "stopped" {
		t.Fatalf("expected state=stopped, got %s", state.StatefulPanes["%1"].State)
	}
	if _, ok := state.WaitingPanes["%1"]; ok {
		t.Fatal("expected stopped pane NOT in WaitingPanes")
	}
	if state.WaitingWindows["@1"] != 0 {
		t.Fatalf("expected WaitingWindows count=0 for stopped pane, got %d", state.WaitingWindows["@1"])
	}
}

// ---------------------------------------------------------------------------
// Window state aggregation tests
// ---------------------------------------------------------------------------

// TestComputeWindowStatesWaitingWins verifies waiting beats all other states.
func TestComputeWindowStatesWaitingWins(t *testing.T) {
	panes := map[string]record{
		"%1": {State: "working"},
		"%2": {State: "waiting"},
		"%3": {State: "stopped"},
		"%4": {State: "done"},
	}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@1", "%3": "@1", "%4": "@1"}
	states := computeWindowStates(panes, paneToWindow)
	if states["@1"] != "waiting" {
		t.Fatalf("expected window state=waiting, got %s", states["@1"])
	}
}

// TestComputeWindowStatesWorkingBeatsStopped verifies working beats stopped and done.
func TestComputeWindowStatesWorkingBeatsStopped(t *testing.T) {
	panes := map[string]record{
		"%1": {State: "working"},
		"%2": {State: "stopped"},
		"%3": {State: "done"},
	}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@1", "%3": "@1"}
	states := computeWindowStates(panes, paneToWindow)
	if states["@1"] != "working" {
		t.Fatalf("expected window state=working, got %s", states["@1"])
	}
}

// TestComputeWindowStatesWorkingBeatsDone verifies working beats done.
func TestComputeWindowStatesWorkingBeatsDone(t *testing.T) {
	panes := map[string]record{
		"%1": {State: "done"},
		"%2": {State: "working"},
	}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@1"}
	states := computeWindowStates(panes, paneToWindow)
	if states["@1"] != "working" {
		t.Fatalf("expected window state=working, got %s", states["@1"])
	}
}

// TestComputeWindowStatesSingleDone verifies a single done pane produces done.
func TestComputeWindowStatesSingleDone(t *testing.T) {
	panes := map[string]record{
		"%1": {State: "done"},
	}
	paneToWindow := map[string]string{"%1": "@1"}
	states := computeWindowStates(panes, paneToWindow)
	if states["@1"] != "done" {
		t.Fatalf("expected window state=done, got %s", states["@1"])
	}
}

// TestComputeWindowStatesEmpty verifies no panes produces empty map.
func TestComputeWindowStatesEmpty(t *testing.T) {
	states := computeWindowStates(map[string]record{}, map[string]string{})
	if len(states) != 0 {
		t.Fatalf("expected empty window states, got %v", states)
	}
}

// TestComputeWindowStatesMultipleWindows verifies independent windows get their own states.
func TestComputeWindowStatesMultipleWindows(t *testing.T) {
	panes := map[string]record{
		"%1": {State: "working"},
		"%2": {State: "waiting"},
		"%3": {State: "done"},
	}
	paneToWindow := map[string]string{"%1": "@1", "%2": "@2", "%3": "@1"}
	states := computeWindowStates(panes, paneToWindow)
	if states["@1"] != "working" {
		t.Fatalf("expected window @1 state=working, got %s", states["@1"])
	}
	if states["@2"] != "waiting" {
		t.Fatalf("expected window @2 state=waiting, got %s", states["@2"])
	}
}

// ---------------------------------------------------------------------------
// renderWindowSegment tests
// ---------------------------------------------------------------------------

func defaultSegmentConfig() segmentConfig {
	return parseSegmentConfig(map[string]string{})
}

func TestRenderWindowSegmentEmpty(t *testing.T) {
	got := renderWindowSegment("", false, "0", "", defaultSegmentConfig())
	if got != "" {
		t.Fatalf("expected empty segment for empty state, got %q", got)
	}
}

func TestRenderWindowSegmentWaitingNoFlash(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("waiting", false, "0", "", cfg)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("expected waiting icon, got %q", got)
	}
	if !strings.Contains(got, "fg=yellow") {
		t.Fatalf("expected yellow fg for waiting, got %q", got)
	}
	if strings.Contains(got, "bg=colour226") {
		t.Fatalf("expected no flash bg when not flashing, got %q", got)
	}
}

func TestRenderWindowSegmentWorkingNoFlash(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("working", false, "0", "✻", cfg)
	if !strings.Contains(got, "✻") {
		t.Fatalf("expected spinner frame, got %q", got)
	}
	if !strings.Contains(got, "fg=#d88786") {
		t.Fatalf("expected working color, got %q", got)
	}
}

func TestRenderWindowSegmentStoppedNoFlash(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("stopped", false, "0", "", cfg)
	if !strings.Contains(got, "⏸") {
		t.Fatalf("expected stopped icon, got %q", got)
	}
	if !strings.Contains(got, "fg=colour196") {
		t.Fatalf("expected stopped color, got %q", got)
	}
}

func TestRenderWindowSegmentDoneNoFlash(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("done", false, "0", "", cfg)
	if !strings.Contains(got, "✓") {
		t.Fatalf("expected done icon, got %q", got)
	}
	if !strings.Contains(got, "fg=green") {
		t.Fatalf("expected done color, got %q", got)
	}
}

func TestRenderWindowSegmentFlashPhase1(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("waiting", true, "1", "", cfg)
	if !strings.Contains(got, "bg=colour226") {
		t.Fatalf("expected flash bg during phase 1, got %q", got)
	}
	if !strings.Contains(got, "fg=colour16") {
		t.Fatalf("expected flash fg during phase 1, got %q", got)
	}
	if strings.Contains(got, "fg=yellow") {
		t.Fatalf("expected icon color suppressed during flash phase 1, got %q", got)
	}
}

func TestRenderWindowSegmentFlashPhase0(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("waiting", true, "0", "", cfg)
	if strings.Contains(got, "bg=colour226") {
		t.Fatalf("expected no flash bg during phase 0, got %q", got)
	}
	if !strings.Contains(got, "fg=yellow") {
		t.Fatalf("expected normal icon color during flash phase 0, got %q", got)
	}
}

func TestRenderWindowSegmentCustomColors(t *testing.T) {
	cfg := segmentConfig{
		iconWaiting:  "!",
		colorWaiting: "red",
		colorFlashBg: "blue",
		colorFlashFg: "white",
		colorTextFg:  "grey",
	}
	got := renderWindowSegment("waiting", false, "0", "", cfg)
	if !strings.Contains(got, "!") {
		t.Fatalf("expected custom icon, got %q", got)
	}
	if !strings.Contains(got, "fg=red") {
		t.Fatalf("expected custom color, got %q", got)
	}
	if !strings.Contains(got, "fg=grey") {
		t.Fatalf("expected custom text fg, got %q", got)
	}
}

func TestRenderWindowSegmentTextFgReset(t *testing.T) {
	cfg := defaultSegmentConfig()
	got := renderWindowSegment("done", false, "0", "", cfg)
	if !strings.Contains(got, "fg=colour255") {
		t.Fatalf("expected text fg reset at end, got %q", got)
	}
}

func TestRenderWindowSegmentInheritedTextStyle(t *testing.T) {
	cfg := defaultSegmentConfig()
	cfg.colorTextFg = "default"
	got := renderWindowSegment("done", false, "0", "", cfg)
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("expected inherited text style reset at end, got %q", got)
	}
}

// contains is a test helper that checks whether a string slice contains a value.
func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// TestBuildFlashStateIgnoresForeignSocket verifies that a record stamped with a
// different tmux server's socket is ignored even when its pane ID collides with
// a pane in this daemon's topology — the cross-server bleed this scoping fixes.
func TestBuildFlashStateIgnoresForeignSocket(t *testing.T) {
	// %15 exists in this daemon, but the waiting record was written by an agent
	// inside another tmux server whose %15 is unrelated.
	records := []record{{
		Agent:     "claude",
		PaneID:    "%15",
		Reason:    "permission_prompt",
		UpdatedAt: 100,
		State:     "waiting",
		Socket:    "/tmp/tmux-test/other",
	}}
	paneToWindow := map[string]string{"%15": "@2"}
	windows := map[string]struct{}{"@2": {}}
	activeWindows := map[string]struct{}{"@2": {}}
	seenAt := map[string]int64{"@2": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "/tmp/tmux-test/outer")

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected foreign-socket record to be ignored, got stateful panes: %v", state.StatefulPanes)
	}
	if state.WindowsFlashing["@2"] {
		t.Fatal("window must not flash for a record owned by a different tmux server")
	}
}

// TestBuildFlashStateMatchesOwnSocket verifies that a record stamped with this
// daemon's own socket is honored, and that an empty socket (legacy producer)
// still matches any daemon for backward compatibility.
func TestBuildFlashStateMatchesOwnSocket(t *testing.T) {
	const sock = "/tmp/tmux-test/outer"
	for _, tc := range []struct {
		name   string
		socket string
	}{
		{"own socket", sock},
		{"legacy empty socket", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []record{{
				Agent:     "relay",
				PaneID:    "%15",
				Reason:    "external_relay",
				UpdatedAt: 100,
				State:     "waiting",
				Socket:    tc.socket,
			}}
			paneToWindow := map[string]string{"%15": "@2"}
			windows := map[string]struct{}{"@2": {}}
			activeWindows := map[string]struct{}{"@2": {}}
			seenAt := map[string]int64{"@2": 0}

			state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, sock)

			if state.WaitingWindows["@2"] != 1 {
				t.Fatalf("expected window @2 to be waiting, got %d", state.WaitingWindows["@2"])
			}
		})
	}
}

func TestBuildFlashStateIgnoresRetiredTerminalBellRecords(t *testing.T) {
	records := []record{
		{
			Agent:     "codex",
			PaneID:    "%15",
			Reason:    "agent-stop",
			UpdatedAt: 100,
			State:     "done",
		},
		{
			Agent:     "codex",
			PaneID:    "%15",
			Reason:    "terminal-bell",
			UpdatedAt: 101,
			State:     "waiting",
		},
	}
	paneToWindow := map[string]string{"%15": "@2"}
	windows := map[string]struct{}{"@2": {}}
	activeWindows := map[string]struct{}{"@2": {}}
	seenAt := map[string]int64{"@2": 0}

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3, "")

	if got := state.StatefulPanes["%15"].State; got != "done" {
		t.Fatalf("expected direct lifecycle state to win, got %q", got)
	}
	if state.WaitingWindows["@2"] != 0 || state.WindowsFlashing["@2"] {
		t.Fatal("retired terminal-bell record must not create waiting state")
	}
}

func TestParsePaneLinesReadsWindowSeenAt(t *testing.T) {
	topo := parsePaneLines("%1\t@1\t1700\tclaude\t/src\n%2\t@1\t1700\tzsh\t/src\n%3\t@2\t\tzsh\t/tmp\n")
	if got := topo.seenAtByWindow["@1"]; got != 1700 {
		t.Fatalf("seenAt @1 = %d, want 1700", got)
	}
	if got := topo.seenAtByWindow["@2"]; got != 0 {
		t.Fatalf("seenAt @2 = %d, want 0", got)
	}
	if topo.paneToWindow["%2"] != "@1" || len(topo.panes) != 3 || len(topo.windows) != 2 {
		t.Fatalf("unexpected topology: %+v", topo)
	}
	if got := topo.paneIdentities["%1"]; got != (paneIdentity{Command: "claude", CWD: "/src"}) {
		t.Fatalf("identity %%1 = %+v", got)
	}
}

// fakeTmux replaces tmux for one test, records each invocation, and answers
// the reads a sync makes with a one-window, one-pane server.
type fakeTmux struct {
	calls   [][]string
	globals string
	clients string
	// windowFormats answers per-window show-options calls.
	windowFormats string
	err           error
}

func installFakeTmux(t *testing.T) *fakeTmux {
	t.Helper()
	f := &fakeTmux{
		globals: "@ai_attn_refresh_client on\n@ai_attn_spinner_frames a,b\n",
		clients: "@1\tattached,focused\n",
	}
	t.Cleanup(func() { tmux = execTmux })
	tmux = func(socket string, args ...string) (string, error) {
		f.calls = append(f.calls, args)
		switch args[0] {
		case "show-options":
			if contains(args, "-t") {
				return f.windowFormats, nil
			}
			return f.globals, nil
		case "list-panes":
			return "%1\t@1\t0\tclaude\t/src\n", nil
		case "list-clients":
			return f.clients, nil
		}
		return "", f.err
	}
	return f
}

func (f *fakeTmux) commands() []string {
	names := make([]string, len(f.calls))
	for i, call := range f.calls {
		names[i] = call[0]
	}
	return names
}

func newWorkingSyncer() *syncer {
	return &syncer{
		socket:        "/tmp/tmux-ai-attn-test",
		cachedPayload: listPayload{Records: []record{{Agent: "claude", PaneID: "%1", State: "working", UpdatedAt: 1}}},
		hasCached:     true,
	}
}

func TestSpinnerTickReusesRecentTmuxReads(t *testing.T) {
	f := installFakeTmux(t)
	s := newWorkingSyncer()

	s.syncOnce(false)
	if got, want := strings.Join(f.commands(), " "), "show-options list-panes list-clients show-options set-window-option"; got != want {
		t.Fatalf("first sync ran %q, want %q", got, want)
	}

	// Force a frame change so the tick has something to publish.
	f.calls = nil
	s.globals["@ai_attn_spinner_frame"] = "x"
	s.syncOnce(false)
	if len(f.calls) != 1 {
		t.Fatalf("spinner tick ran %d tmux commands (%q), want 1", len(f.calls), f.commands())
	}
	call := f.calls[0]
	if contains(call, "@ai_attn_state_hash") || contains(call, "@ai_attn_pane_state") {
		t.Fatalf("spinner tick rewrote stable state: %q", call)
	}
	if got := strings.Join(call[len(call)-3:], " "); got != "; refresh-client -S" {
		t.Fatalf("spinner tick ended with %q, want the refresh", got)
	}
	if s.globals["@ai_attn_spinner_frame"] == "x" {
		t.Fatal("cached spinner frame not updated after the write")
	}
}

func TestSpinnerTickRereadsStaleTmuxReads(t *testing.T) {
	f := installFakeTmux(t)
	s := newWorkingSyncer()
	s.syncOnce(false)

	f.calls = nil
	s.readAt = time.Now().Add(-tmuxReadMaxAge)
	s.globals["@ai_attn_spinner_frame"] = "x"
	s.syncOnce(false)
	if got := strings.Join(f.commands(), " "); !strings.HasPrefix(got, "show-options list-panes list-clients") {
		t.Fatalf("stale tick ran %q, want fresh reads first", got)
	}
}

func TestAttnChangeRereadsTmux(t *testing.T) {
	f := installFakeTmux(t)
	cli := filepath.Join(t.TempDir(), "ai-attn")
	script := "#!/bin/sh\necho '{\"records\":[{\"agent\":\"claude\",\"pane_id\":\"%1\",\"state\":\"working\",\"updated_at\":1}]}'\n"
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.globals += "@ai_attn_cli " + cli + "\n"
	s := newWorkingSyncer()
	s.syncOnce(false)

	f.calls = nil
	s.syncOnce(true)
	if got := strings.Join(f.commands(), " "); !strings.HasPrefix(got, "show-options list-panes list-clients") {
		t.Fatalf("ai-attn change ran %q, want fresh reads first", got)
	}
}

func TestFailedUpdateDropsCachedTmuxReads(t *testing.T) {
	f := installFakeTmux(t)
	s := newWorkingSyncer()
	f.err = errors.New("tmux set-window-option: timed out after 5s")
	s.syncOnce(false)
	if s.globals != nil || s.topo != nil {
		t.Fatal("cache kept after a failed update")
	}

	f.calls, f.err = nil, nil
	s.syncOnce(false)
	if got := strings.Join(f.commands(), " "); !strings.HasPrefix(got, "show-options list-panes list-clients") {
		t.Fatalf("sync after a failure ran %q, want fresh reads first", got)
	}
}

func TestTmuxBatchRefreshIgnoresNoCurrentClient(t *testing.T) {
	f := installFakeTmux(t)
	f.err = errors.New("tmux set-option -gq @a 1 … (2 commands, 30 bytes): no current client")
	if err := tmuxBatchRefresh("s", []string{"set-option", "-gq", "@a", "1"}, true); err != nil {
		t.Fatalf("tmuxBatchRefresh = %v, want nil", err)
	}
	if err := tmuxBatchRefresh("s", []string{"set-option", "-gq", "@a", "1"}, false); err == nil {
		t.Fatal("tmuxBatchRefresh without refresh swallowed the error")
	}
}

func TestTmuxBatchRefreshRefreshesOnceInFinalChunk(t *testing.T) {
	f := installFakeTmux(t)
	var batch []string
	for i := 0; i < 200; i++ {
		batch = appendGlobalOption(batch, fmt.Sprintf("@tat_%d", i), strings.Repeat("x", 80))
	}
	if err := tmuxBatchRefresh("s", batch, true); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) < 2 {
		t.Fatalf("batch sent in %d invocations, want it chunked", len(f.calls))
	}
	for i, call := range f.calls {
		if got := contains(call, "refresh-client"); got != (i == len(f.calls)-1) {
			t.Fatalf("invocation %d of %d has refresh-client = %v", i+1, len(f.calls), got)
		}
	}
	if last := f.calls[len(f.calls)-1]; last[len(last)-1] != "-S" {
		t.Fatalf("final invocation ends with %q, want the refresh last", last[len(last)-1])
	}
}

func TestApplyGlobalWritesRecordsOnlyGlobalSets(t *testing.T) {
	globals := map[string]string{"@ai_attn_state_hash": "old"}
	batch := appendWindowOption(nil, "@1", "@ai_attn_window_segment", "seg")
	batch = appendGlobalOption(batch, "@ai_attn_state_hash", "new")
	batch = appendPaneOption(batch, "%1", "@ai_attn_pane_state", "working")
	batch = appendGlobalOption(batch, "@ai_attn_spinner_frame", "")
	applyGlobalWrites(globals, batch)
	want := map[string]string{"@ai_attn_state_hash": "new", "@ai_attn_spinner_frame": ""}
	if !reflect.DeepEqual(globals, want) {
		t.Fatalf("globals = %v, want %v", globals, want)
	}
}

func TestPaceForFollowsWhatTheStatusLineCanShow(t *testing.T) {
	refreshing := map[string]string{"@ai_attn_refresh_client": "on", "status-interval": "1", "focus-events": "on"}
	if got := paceFor(refreshing, 0, false); !got.paused {
		t.Fatalf("no clients: %+v, want paused", got)
	}
	if got := paceFor(refreshing, 1, true); got != (displayPace{}) {
		t.Fatalf("focused and refreshing: %+v, want full speed", got)
	}
	if got := paceFor(refreshing, 2, false); got.minTick != unfocusedTick {
		t.Fatalf("unfocused: %+v, want %v", got, unfocusedTick)
	}
	// Without focus-events, tmux cannot tell, so nothing slows down.
	noFocusEvents := map[string]string{"@ai_attn_refresh_client": "on", "focus-events": "off"}
	if got := paceFor(noFocusEvents, 1, false); got != (displayPace{}) {
		t.Fatalf("focus-events off: %+v, want full speed", got)
	}
	// Without forced redraws, frames faster than status-interval go unseen.
	timed := map[string]string{"@ai_attn_refresh_client": "off", "status-interval": "2", "focus-events": "on"}
	if got := paceFor(timed, 1, true); got.minTick != 2*time.Second {
		t.Fatalf("refresh off: %+v, want 2s", got)
	}
	never := map[string]string{"@ai_attn_refresh_client": "off", "status-interval": "0"}
	if got := paceFor(never, 1, true); !got.paused {
		t.Fatalf("refresh off and status-interval 0: %+v, want paused", got)
	}
}

func TestTickForStatePacesOnlyAnimation(t *testing.T) {
	working := flashState{StatefulPanes: map[string]record{"%1": {State: "working"}}}
	waiting := flashState{StatefulPanes: map[string]record{"%1": {State: "done"}}}
	if got := tickForState(working, 120, 4, displayPace{paused: true}); got != pausedTick {
		t.Fatalf("paused spinner ticks every %v, want %v", got, pausedTick)
	}
	if got := tickForState(working, 120, 4, displayPace{minTick: time.Second}); got != time.Second {
		t.Fatalf("slowed spinner ticks every %v, want 1s", got)
	}
	if got := tickForState(working, 2000, 4, displayPace{minTick: time.Second}); got != 2*time.Second {
		t.Fatalf("a spinner slower than the floor ticks every %v, want 2s", got)
	}
	if got := tickForState(waiting, 120, 4, displayPace{paused: true}); got != ageTick {
		t.Fatalf("age updates tick every %v while paused, want %v", got, ageTick)
	}
	if got := tickForState(flashState{}, 120, 4, displayPace{paused: true}); got != 0 {
		t.Fatalf("idle ticks every %v, want 0", got)
	}
}

func TestParseClientLinesCountsClientsAndFocus(t *testing.T) {
	windows, clients, focused := parseClientLines("@1\tattached,focused,UTF-8\n@2\tattached,UTF-8\n\n")
	if clients != 2 || !focused || len(windows) != 2 {
		t.Fatalf("got %v %d %v", windows, clients, focused)
	}
	_, clients, focused = parseClientLines("@1\tattached,UTF-8\n")
	if clients != 1 || focused {
		t.Fatalf("unfocused: got %d %v", clients, focused)
	}
	if _, clients, _ := parseClientLines(""); clients != 0 {
		t.Fatalf("no clients: got %d", clients)
	}
}

func TestSyncPausesTheSpinnerWithoutClients(t *testing.T) {
	f := installFakeTmux(t)
	f.clients = ""
	s := newWorkingSyncer()
	if _, tick := s.syncOnce(false); tick != pausedTick {
		t.Fatalf("tick without clients = %v, want %v", tick, pausedTick)
	}
}

func TestGlobalOptionsIncludeWhatPacingNeeds(t *testing.T) {
	f := installFakeTmux(t)
	f.globals = "@ai_attn_tick_ms 120\nstatus-interval 1\nstatus on\nfocus-events on\nescape-time 10\n"
	globals, err := tmuxGetGlobalOptions("s", "@ai_attn_")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"@ai_attn_tick_ms": "120", "status-interval": "1", "focus-events": "on"}
	if len(globals) != len(want) {
		t.Fatalf("got %v, want %v", globals, want)
	}
	for name, value := range want {
		if globals[name] != value {
			t.Fatalf("%s = %q, want %q", name, globals[name], value)
		}
	}
	if got := strings.Join(f.calls[0], " "); got != "show-options -gq ; show-options -sq ; show-options -gwq" {
		t.Fatalf("read with %q, want one call for all three", got)
	}
}

func TestReadsSegmentRawTellsExpandingReadsApart(t *testing.T) {
	for format, raw := range map[string]bool{
		"#[fg=colour255]#{E:@ai_attn_window_segment}#W":               false,
		"#{T:@ai_attn_window_segment}":                                false,
		"#{E;s|#\\[[^]]*\\]||:@ai_attn_window_segment}":               false,
		"#{s|#\\[[^]]*\\]||:#{E:@ai_attn_window_segment}}":            false,
		"#{?@ai_attn_window_segment,yes,no}":                          false,
		"#I:#W":                                                       false,
		"#{@ai_attn_window_segment}":                                  true,
		"#{s|#\\[[^]]*\\]||:@ai_attn_window_segment}":                 true,
		"#{E:@ai_attn_window_segment} and #{@ai_attn_window_segment}": true,
		"#{=10:@ai_attn_window_segment}":                              true,
	} {
		if got := readsSegmentRaw(format); got != raw {
			t.Errorf("readsSegmentRaw(%q) = %v, want %v", format, got, raw)
		}
	}
}

func TestSegmentReadersExpandChecksEveryFormatOption(t *testing.T) {
	expanding := map[string]string{
		"window-status-format":         "#{E:@ai_attn_window_segment}#W",
		"window-status-current-format": "#{E:@ai_attn_window_segment}#W",
		"@ai_attn_window_segment":      "#{@ai_attn_window_segment}", // not a format
	}
	if !segmentReadersExpand(expanding) {
		t.Fatal("expanding formats judged raw")
	}
	for _, name := range []string{"status-format[1]", "status-right", "pane-border-format"} {
		raw := map[string]string{name: "#{@ai_attn_window_segment}"}
		if segmentReadersExpand(raw) {
			t.Fatalf("raw read in %s missed", name)
		}
	}
}

func TestWindowFormatsReadSegmentRawAsksEveryWindowAtOnce(t *testing.T) {
	f := installFakeTmux(t)
	windows := map[string]struct{}{"@2": {}, "@1": {}}
	raw, err := windowFormatsReadSegmentRaw("s", windows)
	if err != nil || raw {
		t.Fatalf("no window formats: raw=%v err=%v", raw, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("asked in %d calls, want 1", len(f.calls))
	}
	got := strings.Join(f.calls[0], " ")
	for _, want := range []string{"-t @1 window-status-format", "-t @2 window-status-current-format", "-t @2 pane-border-format"} {
		if !strings.Contains(got, want) {
			t.Fatalf("call %q lacks %q", got, want)
		}
	}

	f.windowFormats = "window-status-format \"#{@ai_attn_window_segment}#W\"\n"
	if raw, _ := windowFormatsReadSegmentRaw("s", windows); !raw {
		t.Fatal("a raw read set on one window was missed")
	}
}

func TestSpinnerTickWritesOnlyTheFrameWhenSegmentsReferToIt(t *testing.T) {
	f := installFakeTmux(t)
	s := newWorkingSyncer()
	s.syncOnce(false)
	segment := ""
	for _, call := range f.calls {
		for i, arg := range call {
			if arg == "@ai_attn_window_segment" && i+1 < len(call) {
				segment = call[i+1]
			}
		}
	}
	if !strings.Contains(segment, spinnerFrameRef) {
		t.Fatalf("segment %q does not refer to the frame", segment)
	}

	f.calls = nil
	s.globals["@ai_attn_spinner_frame"] = "x"
	s.syncOnce(false)
	call := strings.Join(f.calls[0], " ")
	if strings.Contains(call, "set-window-option") || !strings.Contains(call, "set-option -gq @ai_attn_spinner_frame") {
		t.Fatalf("spinner tick ran %q, want only the frame and the redraw", call)
	}
}

func TestARawReaderKeepsFramesInTheSegmentsAndRewritesThemOnChange(t *testing.T) {
	f := installFakeTmux(t)
	f.windowFormats = "window-status-format \"#{@ai_attn_window_segment}\"\n"
	s := newWorkingSyncer()
	s.syncOnce(false)
	if s.segmentsFrameRef || !s.segmentsWritten {
		t.Fatalf("raw reader: frameRef=%v written=%v", s.segmentsFrameRef, s.segmentsWritten)
	}
	f.calls = nil
	s.globals["@ai_attn_spinner_frame"] = "x"
	s.syncOnce(false)
	if call := strings.Join(f.calls[0], " "); !strings.Contains(call, "set-window-option") {
		t.Fatalf("spinner tick ran %q, want the window segment written", call)
	}

	// The raw reader goes away: the next check switches modes and rewrites
	// every segment, not just the working ones' frames.
	f.windowFormats = ""
	s.windowFormatsAt = time.Time{}
	s.readAt = time.Time{}
	f.calls = nil
	s.syncOnce(false)
	if !s.segmentsFrameRef {
		t.Fatal("did not switch to frame references")
	}
	if call := strings.Join(f.calls[len(f.calls)-1], " "); !strings.Contains(call, "@ai_attn_state_hash") {
		t.Fatalf("mode switch ran %q, want a full write", call)
	}
}
