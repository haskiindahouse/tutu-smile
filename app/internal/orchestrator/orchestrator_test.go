package orchestrator

import (
	"testing"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
)

// A carried row must not share mutable slice backing arrays with the prior
// board: appends (decision log) and in-place writes (hug-wave swapping an
// alternative) would otherwise race with clients still reading the old board.
func TestCarryRowDoesNotAliasPriorSlices(t *testing.T) {
	prior := event.BoardRow{
		GuestID:      "g1",
		Decisions:    make([]event.DecisionEntry, 1, 8),
		RiskReasons:  make([]string, 1, 4),
		Alternatives: make([]event.RouteOption, 1, 4),
	}
	prior.Alternatives[0] = event.RouteOption{Number: "ALT-1"}

	row := carryRow(prior)

	row.Decisions = append(row.Decisions, decision("recheck", "x"))
	row.RiskReasons = append(row.RiskReasons, "y")
	row.Alternatives[0] = event.RouteOption{Number: "SWAPPED"}

	if len(prior.Decisions) != 1 || len(prior.RiskReasons) != 1 {
		t.Fatal("appends to the carried row leaked into the prior row")
	}
	if prior.Alternatives[0].Number != "ALT-1" {
		t.Fatalf("wave-style swap leaked into the prior board: %s", prior.Alternatives[0].Number)
	}
	// The contents must be fully carried over, not just detached.
	if row.Decisions[0] != prior.Decisions[0] || row.RiskReasons[0] != prior.RiskReasons[0] {
		t.Fatal("carryRow must preserve prior contents")
	}
}
