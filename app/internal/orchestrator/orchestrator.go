// Package orchestrator assembles the live board from the pieces: it fans the
// planner out across guests (bounded concurrency), resolves each chosen route's
// checkout deeplink, spaces arrivals with the hug-wave, finds opt-in companions,
// computes honest totalizator odds, and diffs against the previous board so a
// route that has collapsed and been rebuilt is shown as пересобран, not silently
// swapped.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/companions"
	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/llm"
	"github.com/ma4ypic4y/tutu-smile/internal/planner"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
	"github.com/ma4ypic4y/tutu-smile/internal/wave"
)

type Orchestrator struct {
	plan    *planner.Planner
	svc     *tutu.Service
	llm     *llm.Client
	maxConc int
}

func New(p *planner.Planner, svc *tutu.Service, l *llm.Client, maxConc int) *Orchestrator {
	if maxConc < 1 {
		maxConc = 4
	}
	return &Orchestrator{plan: p, svc: svc, llm: l, maxConc: maxConc}
}

// BuildBoard computes a full board for the event. fresh forces live searches
// (used by the recheck loop). prior is the last board, used to detect collapses.
func (o *Orchestrator) BuildBoard(ctx context.Context, ev event.Event, prior *event.Board, fresh bool) *event.Board {
	priorRows := map[string]event.BoardRow{}
	if prior != nil {
		for _, r := range prior.Rows {
			priorRows[r.GuestID] = r
		}
	}

	rows := make([]event.BoardRow, len(ev.Guests))
	sem := make(chan struct{}, o.maxConc)
	var wg sync.WaitGroup

	for i, g := range ev.Guests {
		wg.Add(1)
		go func(i int, g event.Guest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = o.planRow(ctx, ev, g, priorRows[g.ID], fresh)
		}(i, g)
	}
	wg.Wait()

	deadline, _ := ev.DeadlineTime()

	// Hug-wave: space arrivals so one greeter can meet everyone.
	wave.Apply(rows, ev.SpacingMin, deadline)

	// Re-evaluate risk against the FINAL chosen route (the wave may have swapped
	// a guest onto a tighter alternative).
	reclassifyRisk(rows, ev)

	// Companions among opt-in guests, enriched with live «места рядом» from the
	// rail seat map (task=together) — a concrete car and adjacent seats.
	comps := companions.Find(ev, rows)
	o.attachSeatHints(ctx, comps, rows)

	board := &event.Board{
		EventID:     ev.ID,
		Rows:        rows,
		Companions:  comps,
		Destination: ev.Destination,
		UpdatedAt:   time.Now(),
		Total:       len(rows),
	}
	if d, ok := ev.DeadlineTime(); ok {
		board.Deadline = d
	}
	if g, ok := ev.GatherTime(); ok {
		board.GatherAt = g
	}
	if c, ok := tutu.CityCoord(ev.Destination); ok {
		board.DestCoord = &c
	}
	for _, r := range rows {
		switch r.Status {
		case event.StatusAssembled, event.StatusReassembled, event.StatusPurchased:
			board.Assembled++
		}
	}
	if ev.Totalizator {
		board.Bets = computeBets(rows)
	}
	// Stable order: by arrival time, unresolved rows last.
	sortRows(board.Rows)
	return board
}

func (o *Orchestrator) planRow(ctx context.Context, ev event.Event, g event.Guest, prior event.BoardRow, fresh bool) event.BoardRow {
	// A purchased ticket freezes the row: live inventory no longer matters —
	// the seat is theirs. The prior row is carried as-is, only the status flips.
	if g.Purchased && prior.Chosen != nil {
		row := carryRow(prior)
		row.Purchased = true
		if row.Status != event.StatusPurchased {
			row.Status = event.StatusPurchased
			row.Decisions = append(row.Decisions, decision("purchased",
				"гость подтвердил покупку — маршрут закреплён, перепроверка не нужна"))
		}
		return row
	}

	row := event.BoardRow{
		GuestID:      g.ID,
		GuestName:    g.Name,
		City:         g.City,
		Profile:      g.Profile,
		Status:       event.StatusPlanning,
		NeedsLodging: g.NeedsLodging,
		LastChecked:  time.Now(),
	}
	if c, ok := tutu.CityCoord(g.City); ok {
		row.Coord = &c
	}

	res, err := o.plan.PlanGuest(ctx, ev, g, fresh)
	if err != nil {
		// A transport-level failure during a recheck must not wipe a good row:
		// the guest's route did not die, our view of it did. Keep the prior row
		// and say so in the log.
		if prior.Chosen != nil {
			row = carryRow(prior)
			row.Decisions = capLog(append(row.Decisions, decision("recheck",
				fmt.Sprintf("перепроверка не удалась (%v) — держу прежний маршрут", err))))
			return row
		}
		row.Status = event.StatusNeedsHelp
		row.RiskReasons = []string{err.Error()}
		return row
	}
	row.Status = res.Status
	row.Chosen = res.Chosen
	row.Alternatives = res.Alternatives
	row.RiskReasons = res.RiskReasons
	row.Decisions = res.Decisions
	row.Pinned = g.PinnedKey != "" && !res.PinnedLost && row.Chosen != nil && row.Chosen.Key == g.PinnedKey
	if g.Purchased && row.Chosen != nil {
		// Purchased but no prior board (fresh server) — freeze on what we found.
		row.Purchased = true
		row.Status = event.StatusPurchased
	}

	// Collapse detection: compare against the previous board.
	if prior.Chosen != nil {
		switch {
		case row.Chosen == nil:
			row.Status = event.StatusNeedsHelp
			row.Decisions = append(row.Decisions, decision("collapsed",
				fmt.Sprintf("маршрут «%s %s» рассыпался, живой замены к дедлайну нет",
					prior.Chosen.ModeHuman, prior.Chosen.Number)))
		case routeChanged(prior.Chosen, row.Chosen):
			was := prior.Chosen
			row.Decisions = append(row.Decisions, decision("collapsed",
				fmt.Sprintf("прежний маршрут «%s %s» за %.0f₽ больше не проходит (инвентарь живой)",
					was.ModeHuman, was.Number, was.Price)))
			row.Decisions = append(row.Decisions, decision("reassembled",
				fmt.Sprintf("пересобрал на «%s %s» за %.0f₽, прибытие %s",
					row.Chosen.ModeHuman, row.Chosen.Number, row.Chosen.Price, hhmm(row.Chosen.ArrivalAt))))
			if row.Status == event.StatusAssembled {
				row.Status = event.StatusReassembled
			}
		}
	}

	// Resolve the checkout deeplink for the chosen route (full chain to Tutu).
	if row.Chosen != nil {
		o.resolveCheckout(ctx, row.Chosen)
		// Human card: reuse the prior one when the route is unchanged to save
		// LLM calls; otherwise (re)write it.
		if prior.Chosen != nil && !routeChanged(prior.Chosen, row.Chosen) && prior.HumanCard != "" {
			row.HumanCard = prior.HumanCard
		} else {
			row.HumanCard = o.writeCard(ctx, ev, g, row.Chosen)
		}
	}

	// Lodging (ТЗ §8): a guest flagged «ночлег» gets live hotel offers by the
	// event — whole-stay prices, the same checkout chain as transport.
	if g.NeedsLodging {
		row.Hotels = o.findHotels(ctx, ev, g, &row)
		if len(row.Hotels) == 0 && len(prior.Hotels) > 0 {
			row.Hotels = prior.Hotels // last known offers beat an empty block
		}
	}
	return row
}

// findHotels searches lodging for one guest: check-in on the event day,
// check-out the morning after. Errors soft-fail into the decision log — a
// missing hotel block never blocks the route row.
func (o *Orchestrator) findHotels(ctx context.Context, ev event.Event, g event.Guest, row *event.BoardRow) []tutu.HotelOffer {
	out := nextDate(ev.Date)
	if out == "" {
		return nil
	}
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	hotels, err := o.svc.SearchHotels(hctx, ev.Destination, ev.Date, out, maxInt(g.Adults, 1), 0, 3)
	if err != nil {
		row.Decisions = append(row.Decisions, decision("hotel",
			fmt.Sprintf("ночлег: поиск отелей не удался (%v) — покажу при следующей проверке", err)))
		return nil
	}
	if len(hotels) == 0 {
		row.Decisions = append(row.Decisions, decision("hotel", "ночлег: отелей у места события не нашлось"))
		return nil
	}
	best := hotels[0]
	row.Decisions = append(row.Decisions, decision("hotel",
		fmt.Sprintf("подобрал ночлег: %d варианта(ов), лучший «%s» от %.0f₽ за ночь с %s", len(hotels), best.Name, best.Price, ev.Date)))
	return hotels
}

func nextDate(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// attachSeatHints asks the live rail seat map whether each companion pair can
// sit together, and quotes the cheapest concrete proposal («вагон 8, места 34
// и 36»). Bounded to the first few pairs; any failure just leaves the hint
// empty — the match itself already stands.
func (o *Orchestrator) attachSeatHints(ctx context.Context, comps []event.Companion, rows []event.BoardRow) {
	const maxPairs = 3
	done := 0
	for i := range comps {
		if done >= maxPairs {
			return
		}
		c := &comps[i]
		if c.Mode != tutu.ModeRail {
			continue
		}
		var ref json.RawMessage
		for _, r := range rows {
			if r.GuestID == c.GuestA && r.Chosen != nil && r.Chosen.Number == c.Number {
				ref = r.Chosen.DetailsRef
				break
			}
		}
		if len(ref) == 0 {
			continue
		}
		done++
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		groups, err := o.svc.SeatsTogether(sctx, ref, 2)
		cancel()
		if err != nil || len(groups) == 0 {
			continue
		}
		c.SeatHint = groups[0].Human()
	}
}

func (o *Orchestrator) resolveCheckout(ctx context.Context, opt *event.RouteOption) {
	if opt.CheckoutURL != "" || len(opt.CheckoutRef) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if link, err := o.svc.CreateCheckoutLink(cctx, opt.CheckoutRef); err == nil && link.CheckoutURL != "" {
		opt.CheckoutURL = link.CheckoutURL
	}
}

func (o *Orchestrator) writeCard(ctx context.Context, ev event.Event, g event.Guest, opt *event.RouteOption) string {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	deadline := ""
	if d, ok := ev.GatherTime(); ok {
		deadline = d.Format("15:04")
	}
	return o.llm.WriteCard(cctx, llm.HumanCardInput{
		GuestName:   g.Name,
		FromCity:    g.City,
		ToCity:      ev.Destination,
		Mode:        opt.ModeHuman,
		Number:      opt.Number,
		DepartureAt: opt.DepartureAt.Format(time.RFC3339),
		ArrivalAt:   opt.ArrivalAt.Format(time.RFC3339),
		Price:       opt.Price,
		Transfers:   opt.Transfers,
		NightBefore: opt.NightBefore,
		Deadline:    deadline,
	})
}

// routeChanged reports whether two chosen options are materially different.
func routeChanged(a, b *event.RouteOption) bool {
	if a == nil || b == nil {
		return a != b
	}
	if a.Mode != b.Mode || a.Number != b.Number {
		return true
	}
	if !a.DepartureAt.Equal(b.DepartureAt) {
		return true
	}
	// A meaningful price move also counts as a change (budget-relevant).
	if diff := a.Price - b.Price; diff > 1 || diff < -1 {
		return true
	}
	return false
}

// computeBets derives an honest late-probability per guest from route fragility.
func computeBets(rows []event.BoardRow) []event.Bet {
	var bets []event.Bet
	for _, r := range rows {
		if r.Chosen == nil {
			bets = append(bets, event.Bet{
				GuestID: r.GuestID, GuestName: r.GuestName,
				LateChance: 0.9, Rationale: "маршрут ещё не собран",
			})
			continue
		}
		p := 0.05
		reason := "прямой маршрут с запасом"
		if r.Purchased {
			// A bought ticket removes the "will they even go" fragility; only
			// the physical route risk remains, capped low.
			bets = append(bets, event.Bet{
				GuestID: r.GuestID, GuestName: r.GuestName,
				LateChance: purchasedChance(r), Rationale: "билет куплен — остаётся только дорога",
			})
			continue
		}
		if r.Chosen.Transfers > 0 {
			p += 0.12 * float64(r.Chosen.Transfers)
			reason = fmt.Sprintf("%d пересадк(и)", r.Chosen.Transfers)
		}
		if r.Chosen.NightBefore {
			p += 0.08
			reason += ", ночной перегон"
		}
		switch r.Chosen.Mode {
		case tutu.ModeBus:
			p += 0.10
			reason += ", автобус (пробки)"
		case tutu.ModeEtrain:
			p += 0.06
		}
		if r.Chosen.MarginMin < 90 {
			p += 0.20
			reason += fmt.Sprintf(", запас всего %d мин", r.Chosen.MarginMin)
		} else if r.Chosen.MarginMin < 180 {
			p += 0.08
		}
		if p > 0.95 {
			p = 0.95
		}
		bets = append(bets, event.Bet{
			GuestID: r.GuestID, GuestName: r.GuestName,
			LateChance: round2(p), Rationale: reason,
		})
	}
	return bets
}

func purchasedChance(r event.BoardRow) float64 {
	p := 0.03
	if r.Chosen.Transfers > 0 {
		p += 0.05 * float64(r.Chosen.Transfers)
	}
	if r.Chosen.MarginMin > 0 && r.Chosen.MarginMin < 90 {
		p += 0.05
	}
	if p > 0.15 {
		p = 0.15
	}
	return round2(p)
}

// reclassifyRisk re-flags rows as риск when the FINAL chosen route (post-wave)
// arrives too tight or blows the budget, without disturbing needs-help,
// waiting, reassembled or purchased rows.
func reclassifyRisk(rows []event.BoardRow, ev event.Event) {
	const riskMarginMin = 90
	for i := range rows {
		r := &rows[i]
		if r.Chosen == nil || r.Purchased {
			continue
		}
		if r.Status != event.StatusAssembled && r.Status != event.StatusReassembled {
			continue
		}
		party := 1
		for _, g := range ev.Guests {
			if g.ID == r.GuestID {
				party = g.Party()
			}
		}
		tight := r.Chosen.MarginMin > 0 && r.Chosen.MarginMin < riskMarginMin
		overBudget := ev.BudgetPerP > 0 && r.Chosen.Price > float64(ev.BudgetPerP)*float64(party)
		if tight || overBudget {
			if r.Status == event.StatusAssembled {
				r.Status = event.StatusRisk
			}
			if tight && !hasReason(r.RiskReasons, "впритык") {
				r.RiskReasons = append(r.RiskReasons, fmt.Sprintf("прибывает впритык — запас %d мин до сбора", r.Chosen.MarginMin))
			}
			if overBudget && !hasReason(r.RiskReasons, "бюджет") {
				r.RiskReasons = append(r.RiskReasons, fmt.Sprintf("дороже бюджета: %.0f₽ на %d чел.", r.Chosen.Price, party))
			}
		}
	}
}

func hasReason(reasons []string, sub string) bool {
	for _, r := range reasons {
		if containsFold(r, sub) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func sortRows(rows []event.BoardRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		ai, aj := arrivalKey(rows[i]), arrivalKey(rows[j])
		return ai.Before(aj)
	})
}

func arrivalKey(r event.BoardRow) time.Time {
	if r.Chosen != nil && !r.Chosen.ArrivalAt.IsZero() {
		return r.Chosen.ArrivalAt
	}
	return time.Now().Add(100 * 365 * 24 * time.Hour) // unresolved rows sink
}

func decision(kind, detail string) event.DecisionEntry {
	return event.DecisionEntry{At: time.Now(), Kind: kind, Detail: detail}
}

// carryRow reuses the prior row when a guest's route is frozen (purchased) or
// a recheck failed. The mutable slices are cloned: the prior board may still
// be marshaled to SSE/HTTP clients or read by a concurrent rebuild, so later
// appends and hug-wave swaps must never write into its backing arrays.
func carryRow(prior event.BoardRow) event.BoardRow {
	row := prior
	row.Decisions = append([]event.DecisionEntry(nil), prior.Decisions...)
	row.RiskReasons = append([]string(nil), prior.RiskReasons...)
	row.Alternatives = append([]event.RouteOption(nil), prior.Alternatives...)
	return row
}

// capLog keeps a frozen row's decision log from growing without bound across
// rechecks — the last entries are the ones that matter.
func capLog(log []event.DecisionEntry) []event.DecisionEntry {
	const max = 30
	if len(log) > max {
		return log[len(log)-max:]
	}
	return log
}

func hhmm(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04")
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// EnsureCheckoutRefJSON is a tiny guard used by tests/mocks.
func EnsureCheckoutRefJSON(ref json.RawMessage) bool { return len(ref) > 0 }
