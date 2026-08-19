package wave

import (
	"testing"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

func rowAt(name string, arr time.Time, alts ...event.RouteOption) event.BoardRow {
	return event.BoardRow{
		GuestID: name, GuestName: name, Status: event.StatusAssembled,
		Chosen: &event.RouteOption{
			Mode: tutu.ModeRail, Number: "T-" + name, ArrivalAt: arr,
			Key: event.OptionKey(tutu.ModeRail, "T-"+name, arr.Add(-5*time.Hour)),
		},
		Alternatives: alts,
	}
}

func TestApplySwapsConflictOntoAlternative(t *testing.T) {
	deadline := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	alt := event.RouteOption{Mode: tutu.ModeBus, Number: "B2", ArrivalAt: base.Add(40 * time.Minute)}
	rows := []event.BoardRow{
		rowAt("a", base),
		rowAt("b", base.Add(5*time.Minute), alt), // 5 min after a — conflicts with 20 min gap
	}
	Apply(rows, 20, deadline)
	if rows[1].Chosen.Number != "B2" {
		t.Fatalf("conflicting guest must be moved to the alternative, got %s", rows[1].Chosen.Number)
	}
	if rows[1].WaveShiftMin != 35 {
		t.Fatalf("wave shift must be 35 min, got %d", rows[1].WaveShiftMin)
	}
}

func TestApplyNeverTouchesFrozenRows(t *testing.T) {
	deadline := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	alt := event.RouteOption{Mode: tutu.ModeBus, Number: "B2", ArrivalAt: base.Add(40 * time.Minute)}
	purchased := rowAt("b", base.Add(5*time.Minute), alt)
	purchased.Purchased = true
	rows := []event.BoardRow{rowAt("a", base), purchased}
	Apply(rows, 20, deadline)
	if rows[1].Chosen.Number != "T-b" {
		t.Fatalf("purchased row must never be swapped, got %s", rows[1].Chosen.Number)
	}
	if rows[1].Status != event.StatusAssembled {
		t.Fatalf("purchased row keeps its status, got %s", rows[1].Status)
	}
}

// Regression from a live run: Витя (05:40) conflicted with the 05:01 pair and
// got swapped onto 07:50 — leapfrogging Света (05:45), who then compared
// against 07:50 and caught a phantom 145-minute conflict. The re-sort restart
// must leave Света untouched.
func TestApplySwapDoesNotLeapfrogLaterGuests(t *testing.T) {
	deadline := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 9, 5, h, m, 0, 0, time.UTC) }
	late := event.RouteOption{Mode: tutu.ModeRail, Number: "128М", ArrivalAt: at(7, 50)}
	rows := []event.BoardRow{
		rowAt("nina", at(5, 1)),
		rowAt("vitya", at(5, 10), late), // conflicts with nina → swapped to 07:50
		rowAt("sveta", at(5, 45)),       // 44 min after nina — NO conflict
	}
	Apply(rows, 20, deadline)
	if rows[1].Chosen.ArrivalAt != at(7, 50) {
		t.Fatalf("vitya must be swapped to 07:50, got %v", rows[1].Chosen.ArrivalAt)
	}
	if rows[2].Status != event.StatusAssembled || rows[2].WaveShiftMin != 0 {
		t.Fatalf("sveta must stay untouched (44 min gap), got status=%s shift=%d reasons=%v",
			rows[2].Status, rows[2].WaveShiftMin, rows[2].RiskReasons)
	}
}

func TestApplySameVehicleIsOneArrival(t *testing.T) {
	deadline := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	a := rowAt("a", base)
	b := rowAt("b", base)
	b.Chosen.Number = a.Chosen.Number // same train, same arrival
	rows := []event.BoardRow{a, b}
	Apply(rows, 20, deadline)
	if rows[1].WaveShiftMin != 0 || rows[1].Status != event.StatusAssembled {
		t.Fatal("companions on one vehicle must never be separated by the wave")
	}
}
