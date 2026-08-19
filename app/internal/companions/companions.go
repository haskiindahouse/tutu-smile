// Package companions finds opt-in guests who share a leg of their journey —
// the same train or flight on a common stretch — so the event can start in the
// carriage, not at the table.
//
// Privacy is a hard rule (ТЗ §5): only guests who opted in are considered, and
// a match is symmetric — it exists only when BOTH sides opted in. Names are
// carried here for the organizer's board, but the guest-facing surface must not
// reveal the other party until both consent (enforced at the API layer).
package companions

import (
	"fmt"
	"strings"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

// Find returns shared-segment matches among opt-in guests.
func Find(ev event.Event, rows []event.BoardRow) []event.Companion {
	optIn := map[string]bool{}
	consent := map[string]bool{}
	for _, g := range ev.Guests {
		optIn[g.ID] = g.FindCompanions
		consent[g.ID] = g.CompanionConsent
	}

	var out []event.Companion
	for i := 0; i < len(rows); i++ {
		a := rows[i]
		if !optIn[a.GuestID] || a.Chosen == nil {
			continue
		}
		for j := i + 1; j < len(rows); j++ {
			b := rows[j]
			if !optIn[b.GuestID] || b.Chosen == nil {
				continue
			}
			if shared := sharedSegment(a.Chosen, b.Chosen); shared != "" {
				out = append(out, event.Companion{
					GuestA:  a.GuestID,
					GuestB:  b.GuestID,
					Mode:    a.Chosen.Mode,
					Number:  a.Chosen.Number,
					Segment: shared,
					Note: fmt.Sprintf("%s и %s едут одним %s %s — места можно взять рядом",
						a.GuestName, b.GuestName, modeInstrumental(a.Chosen.Mode), a.Chosen.Number),
					MutualConsent: consent[a.GuestID] && consent[b.GuestID],
				})
			}
		}
	}
	return out
}

// sharedSegment returns a human segment label when two routes coincide on the
// same vehicle (same mode + number), else "".
func sharedSegment(a, b *event.RouteOption) string {
	if a.Mode != b.Mode || a.Number == "" || a.Number != b.Number {
		return ""
	}
	// Same numbered vehicle on the same mode: they are literally together.
	from := cityOf(a.FromStation)
	to := cityOf(a.ToStation)
	if from == "" && to == "" {
		return string(a.Mode)
	}
	return fmt.Sprintf("%s → %s", from, to)
}

func cityOf(station string) string {
	s := strings.TrimSpace(station)
	for _, sep := range []string{" — ", ","} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// ForGuest returns the companion matches visible to a single guest — used by
// the guest-facing card. Per the privacy rule the other party's name stays
// hidden until BOTH sides consented; the seat hint (car + adjacent seats) is
// factual and shows either way.
func ForGuest(all []event.Companion, guestID string) []event.Companion {
	var out []event.Companion
	for _, c := range all {
		if c.GuestA != guestID && c.GuestB != guestID {
			continue
		}
		visible := c
		if !c.MutualConsent {
			visible.Note = fmt.Sprintf("с вами одним %s %s едет ещё один гость (имя — после взаимного согласия)",
				modeInstrumental(c.Mode), c.Number)
			if c.GuestA == guestID {
				visible.GuestB = ""
			} else {
				visible.GuestA = ""
			}
		}
		if c.SeatHint != "" {
			visible.Note += " · " + c.SeatHint
		}
		out = append(out, visible)
	}
	return out
}

func modeInstrumental(m tutu.Mode) string {
	switch m {
	case tutu.ModeRail:
		return "поездом"
	case tutu.ModeBus:
		return "автобусом"
	case tutu.ModeAvia:
		return "рейсом"
	case tutu.ModeEtrain:
		return "электричкой"
	default:
		return m.Human()
	}
}
