package main

import (
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

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

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

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 103, 3)

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

	state, nextSeenAt := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 110, 3)

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

	state, nextSeenAt := buildFlashState(nil, paneToWindow, windows, activeWindows, seenAt, 200, 3)

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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 106, 3)

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
	got := tickForState(state, 120, 4)
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
	got := tickForState(state, 120, 4)
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
	got := tickForState(state, 120, 4)
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
	got := tickForState(state, 120, 4)
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
	got := tickForState(state, 120, 4)
	if got != 0 {
		t.Fatalf("expected 0 tick for idle state, got %v", got)
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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 106, 3)

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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

	if len(state.StatefulPanes) != 0 {
		t.Fatalf("expected no stateful panes for empty PaneID, got %d", len(state.StatefulPanes))
	}
	if len(state.WaitingPanes) != 0 {
		t.Fatalf("expected no waiting panes for empty PaneID, got %d", len(state.WaitingPanes))
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
	got := tickForState(state, 120, 4)
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
	got := tickForState(state, 120, 4)
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

	state, _ := buildFlashState(records, paneToWindow, windows, activeWindows, seenAt, 102, 3)

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

// contains is a test helper that checks whether a string slice contains a value.
func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
