// Package wave implements the "волна обнимашек": one person is meeting every
// guest, so arrivals must be spaced at least N minutes apart. Given each row's
// chosen route plus its alternatives, it nudges conflicting guests onto a
// nearby option that preserves the gap, and flags the ones it cannot separate.
package wave

import (
	"fmt"
	"sort"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
)

// Apply resolves arrival spacing across the board in place. deadline is the
// hard "must be in the city by" instant; no swap may push an arrival past it.
//
// A swap moves a guest to a LATER arrival, which can leapfrog guests that
// sorted after them — so after every swap the walk restarts on the re-sorted
// order (verified live: a stale order manufactured phantom 2-hour conflicts).
// Restarts are bounded: each swap consumes one alternative, so the loop
// terminates.
func Apply(rows []event.BoardRow, spacingMin int, deadline time.Time) {
	if spacingMin <= 0 {
		return
	}
	maxPasses := len(rows) * 3 // every pass either finishes or consumed a swap
	for pass := 0; pass < maxPasses; pass++ {
		if !applyOnce(rows, spacingMin, deadline, false) {
			break // no more swaps — order is settled
		}
	}
	// Final clean pass records the conflicts that truly remain. Marking only
	// here keeps intermediate passes from leaving phantom «waiting» rows.
	applyOnce(rows, spacingMin, deadline, true)
}

// applyOnce walks arrivals in current order; returns true when it performed a
// swap (caller restarts on the new order). mark=true records unresolved
// conflicts (status, reasons, log) instead of swapping — the settling passes
// run with mark=false.
func applyOnce(rows []event.BoardRow, spacingMin int, deadline time.Time, mark bool) bool {
	gap := time.Duration(spacingMin) * time.Minute

	// Only rows with a concrete arrival participate.
	idx := make([]int, 0, len(rows))
	for i := range rows {
		if rows[i].Chosen != nil && !rows[i].Chosen.ArrivalAt.IsZero() {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return rows[idx[a]].Chosen.ArrivalAt.Before(rows[idx[b]].Chosen.ArrivalAt)
	})

	var lastArr time.Time
	var lastKey string
	for pos, i := range idx {
		row := &rows[i]
		key := vehicleKey(row.Chosen)
		if pos == 0 {
			lastArr = row.Chosen.ArrivalAt
			lastKey = key
			continue
		}
		// Guests on the SAME vehicle arrive together — the greeter meets them at
		// once, so they are one arrival, never separated (this is what lets
		// companions travel together).
		if key != "" && key == lastKey {
			continue
		}
		need := lastArr.Add(gap)
		if !row.Chosen.ArrivalAt.Before(need) {
			lastArr = row.Chosen.ArrivalAt
			lastKey = key
			continue
		}

		// A frozen row (ticket purchased, or the guest picked this option
		// themselves) is never swapped: the conflict is recorded and the next
		// guest is spaced from this arrival as it stands.
		if row.Purchased || row.Pinned {
			if mark {
				appendWaveOnce(row, fmt.Sprintf("прибытие ближе %d мин к соседнему, но вариант закреплён %s — не трогаю",
					spacingMin, frozenReason(row)))
			}
			lastArr = row.Chosen.ArrivalAt
			lastKey = key
			continue
		}

		// Conflict: this arrival is too close to the previous one. Try to move
		// this guest onto an alternative that lands at/after `need` and before
		// the deadline, preferring the earliest such option.
		best := -1
		var bestArr time.Time
		if !mark {
			for k, alt := range row.Alternatives {
				if alt.ArrivalAt.IsZero() || alt.ArrivalAt.After(deadline) {
					continue
				}
				if alt.ArrivalAt.Before(need) {
					continue
				}
				if best == -1 || alt.ArrivalAt.Before(bestArr) {
					best, bestArr = k, alt.ArrivalAt
				}
			}
		}
		if best >= 0 {
			prevArr := row.Chosen.ArrivalAt
			swapped := row.Alternatives[best]
			// Put the previous chosen back into the alternatives pool.
			row.Alternatives[best] = *row.Chosen
			row.Chosen = &swapped
			row.WaveShiftMin = int(swapped.ArrivalAt.Sub(prevArr).Minutes())
			row.Decisions = append(row.Decisions, event.DecisionEntry{
				At:   time.Now(),
				Kind: "wave",
				Detail: fmt.Sprintf("сдвинул прибытие на +%d мин, чтобы встречающий успел (зазор %d мин)",
					row.WaveShiftMin, spacingMin),
			})
			// The new arrival may leapfrog guests later in the old order —
			// restart the walk on the re-sorted arrivals.
			return true
		}

		// Couldn't separate: keep the row but mark it waiting-for-decision
		// (only on the final clean pass — settling passes stay silent).
		if mark {
			conflictMin := int(need.Sub(row.Chosen.ArrivalAt).Minutes())
			if row.Status == event.StatusAssembled {
				row.Status = event.StatusWaiting
			}
			reason := fmt.Sprintf("прибытие в пределах %d мин от соседнего — встречающий не успевает", spacingMin)
			if !hasReason(row.RiskReasons, reason) {
				row.RiskReasons = append(row.RiskReasons, reason)
			}
			appendWaveOnce(row, fmt.Sprintf("конфликт волны: нужен зазор ещё %d мин, замены в окне нет", conflictMin))
		}
		// The guest physically arrives when they arrive; the next one is
		// spaced from the latest REAL arrival, not a phantom one.
		if row.Chosen.ArrivalAt.After(lastArr) {
			lastArr = row.Chosen.ArrivalAt
		}
		lastKey = key
	}
	return false
}

// appendWaveOnce logs a wave decision unless the identical line is already
// the row's latest wave entry (restart passes must not spam the log).
func appendWaveOnce(row *event.BoardRow, detail string) {
	for i := len(row.Decisions) - 1; i >= 0; i-- {
		d := row.Decisions[i]
		if d.Kind == "wave" {
			if d.Detail == detail {
				return
			}
			break
		}
	}
	row.Decisions = append(row.Decisions, event.DecisionEntry{At: time.Now(), Kind: "wave", Detail: detail})
}

func hasReason(reasons []string, exact string) bool {
	for _, r := range reasons {
		if r == exact {
			return true
		}
	}
	return false
}

func frozenReason(r *event.BoardRow) string {
	if r.Purchased {
		return "покупкой билета"
	}
	return "выбором гостя"
}

// vehicleKey identifies a concrete vehicle+arrival so guests travelling
// together are recognised as one arrival group.
func vehicleKey(o *event.RouteOption) string {
	if o == nil {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s", o.Mode, o.Number, o.ArrivalAt.Format(time.RFC3339))
}
