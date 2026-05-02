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

// TestTickForStateFlashingReturns1s verifies that tickForState returns 1s when any pane is flashing.
func TestTickForStateFlashingReturns1s(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "working", Agent: "claude"},
			"%2": {State: "waiting", Agent: "codex"},
		},
		PanesFlashing: map[string]bool{
			"%2": true,
		},
	}
	got := tickForState(state)
	if got != 1*time.Second {
		t.Fatalf("expected 1s tick for flashing state, got %v", got)
	}
}

// TestTickForStateWorkingOnlyReturns10s verifies that tickForState returns 10s when
// panes are active but none are flashing.
func TestTickForStateWorkingOnlyReturns10s(t *testing.T) {
	state := flashState{
		StatefulPanes: map[string]record{
			"%1": {State: "working", Agent: "claude"},
		},
		PanesFlashing: map[string]bool{},
	}
	got := tickForState(state)
	if got != 10*time.Second {
		t.Fatalf("expected 10s tick for working-only state (no flash), got %v", got)
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
	got := tickForState(state)
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
	got := tickForState(state)
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
	got := tickForState(state)
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

// TestTickForStateWindowFlashOnly verifies that tickForState returns 1s when
// a window is flashing but no panes are flashing.
func TestTickForStateWindowFlashOnly(t *testing.T) {
	state := flashState{
		StatefulPanes:   map[string]record{"%1": {State: "waiting", Agent: "codex"}},
		PanesFlashing:   map[string]bool{},
		WindowsFlashing: map[string]bool{"@1": true},
	}
	got := tickForState(state)
	if got != 1*time.Second {
		t.Fatalf("expected 1s tick for window-only flash, got %v", got)
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
