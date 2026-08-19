package planner

import (
	"testing"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

func opt(mode tutu.Mode, number string, price float64, durMin int, dep time.Time) event.RouteOption {
	return event.RouteOption{
		Mode: mode, Number: number, Price: price, DurationMin: durMin,
		DepartureAt: dep, ArrivalAt: dep.Add(time.Duration(durMin) * time.Minute),
		Key: event.OptionKey(mode, number, dep),
	}
}

func TestRankOptionsCheaperPrefersPrice(t *testing.T) {
	dep := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	opts := []event.RouteOption{
		opt(tutu.ModeAvia, "SU10", 8000, 90, dep),
		opt(tutu.ModeRail, "128М", 2500, 700, dep),
	}
	rankOptions(opts, event.ProfileCheaper, 0)
	if opts[0].Number != "128М" {
		t.Fatalf("cheaper profile must rank the train first, got %s", opts[0].Number)
	}
	rankOptions(opts, event.ProfileFaster, 0)
	if opts[0].Number != "SU10" {
		t.Fatalf("faster profile must rank the flight first, got %s", opts[0].Number)
	}
}

func TestRankOptionsBudgetSinksOverBudget(t *testing.T) {
	dep := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	opts := []event.RouteOption{
		opt(tutu.ModeAvia, "SU10", 8000, 90, dep), // over budget but fastest
		opt(tutu.ModeBus, "B1", 1500, 400, dep),
	}
	rankOptions(opts, event.ProfileFaster, 3000)
	if opts[0].Number != "B1" {
		t.Fatalf("over-budget option must sink even for faster profile, got %s", opts[0].Number)
	}
}

func TestFindByKeyHonorsGuestPin(t *testing.T) {
	dep := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	opts := []event.RouteOption{
		opt(tutu.ModeRail, "128М", 2500, 700, dep),
		opt(tutu.ModeRail, "026Г", 3200, 650, dep.Add(time.Hour)),
	}
	key := event.OptionKey(tutu.ModeRail, "026Г", dep.Add(time.Hour))
	got := findByKey(opts, key)
	if got == nil || got.Number != "026Г" {
		t.Fatalf("pin lookup failed, got %+v", got)
	}
	if findByKey(opts, "railway|999|nope") != nil {
		t.Fatal("unknown key must return nil (pin lost)")
	}
}

func TestPrevDate(t *testing.T) {
	if got := prevDate("2026-09-05"); got != "2026-09-04" {
		t.Fatalf("prevDate = %s", got)
	}
	if got := prevDate("bad"); got != "" {
		t.Fatalf("prevDate on garbage must be empty, got %s", got)
	}
}
