// Package planner turns the travel search inside out: instead of "how do I get
// there", it answers "how does everyone arrive before the deadline". For each
// guest it searches forward AND on the previous night (night trains), filters
// to arrivals within the deadline minus buffer, ranks by the guest's profile,
// and classifies the row (assembled / risk / needs-help) with a decision log.
package planner

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

// riskMarginMin is the slack below which an on-time route is still flagged
// "риск" — arriving this close to the deadline is engineering-honest, not safe.
const riskMarginMin = 90

type Planner struct {
	svc *tutu.Service
}

func New(svc *tutu.Service) *Planner { return &Planner{svc: svc} }

// Result is a fully classified plan for one guest.
type Result struct {
	Chosen       *event.RouteOption
	Alternatives []event.RouteOption
	Status       event.Status
	RiskReasons  []string
	Decisions    []event.DecisionEntry
	// PinnedLost: the guest had picked an option themselves, and a fresh search
	// no longer sells it — the row was re-ranked and must show «пересобран».
	PinnedLost bool
}

// PlanGuest builds the plan for a single guest against the event's destination.
// fresh bypasses the search cache (used by the recheck scheduler).
func (p *Planner) PlanGuest(ctx context.Context, ev event.Event, g event.Guest, fresh bool) (*Result, error) {
	deadline, ok := ev.DeadlineTime()
	if !ok {
		return nil, fmt.Errorf("event %s: bad date/deadline", ev.ID)
	}
	dest := ev.Destination
	if dest == "" {
		return nil, fmt.Errorf("event %s: no destination", ev.ID)
	}

	var log []event.DecisionEntry
	now := time.Now()
	add := func(kind, detail string) {
		log = append(log, event.DecisionEntry{At: now, Kind: kind, Detail: detail})
	}

	options := map[string]event.RouteOption{} // dedup by mode+number+departure
	var softFails []string
	var multiErr error

	// Pass 1: same-day multitransport, ranked by the guest's profile.
	mr, err := p.svc.SearchMulti(ctx, g.City, dest, ev.Date, g.Party(), g.Profile.Optimize(), nil, fresh)
	if err != nil {
		multiErr = err
		add("error", fmt.Sprintf("мультипоиск %s→%s не удался: %v", g.City, dest, err))
	} else {
		for _, v := range mr.Variants {
			addOption(options, toOption(v, deadline, ev.Date))
		}
		softFails = append(softFails, mr.Unavailable...)
		// Explain a mode that has no direct offers (drives the Калуга moment).
		if rs, ok := mr.ModesSummary["railway"]; ok && rs.Count == 0 {
			if len(rs.Interchange) > 0 {
				add("mode", fmt.Sprintf("прямых поездов из «%s» нет — собираю через пересадку", g.City))
				for _, p := range rs.Interchange {
					addOption(options, interchangeToOption(p))
				}
			} else {
				add("mode", fmt.Sprintf("прямых поездов из «%s» нет — беру другой транспорт", g.City))
			}
		}
		for _, m := range mr.Unavailable {
			add("mode", fmt.Sprintf("режим %s временно недоступен — работаю с остальными", m))
		}
	}

	// Pass 2: night trains — search rail on the previous date; a night train
	// departs the evening before and arrives on the event morning.
	prev := prevDate(ev.Date)
	if prev != "" {
		if rr, err := p.svc.SearchRail(ctx, g.City, dest, prev, g.Party(), fresh); err == nil {
			for _, v := range rr.Offers {
				opt := toOption(v, deadline, ev.Date)
				// Keep only night trains that actually arrive on the event day.
				if !opt.ArrivalAt.IsZero() && sameDay(opt.ArrivalAt, mustDay(ev.Date)) {
					opt.NightBefore = true
					addOption(options, opt)
				}
			}
			// Night-before transfer plans arriving on the event day.
			for _, plan := range rr.Interchange {
				opt := interchangeToOption(plan)
				if !opt.ArrivalAt.IsZero() && sameDay(opt.ArrivalAt, mustDay(ev.Date)) {
					addOption(options, opt)
				}
			}
		} else {
			// Logged but soft-failed: pass 1 may still have produced options, and
			// a total blackout is escalated below via multiErr.
			add("error", fmt.Sprintf("поиск ночных поездов %s→%s на %s не удался: %v", g.City, dest, prev, err))
		}
	}

	// We could not LOOK at the inventory (network/WAF/breaker) and found
	// nothing at all: that is a search failure, not «маршрута нет». Return an
	// error so the orchestrator keeps the guest's previous row intact.
	if len(options) == 0 && multiErr != nil {
		return nil, fmt.Errorf("поиск недоступен: %w", multiErr)
	}

	// Filter to arrivals that make the deadline.
	var inWindow []event.RouteOption
	for _, o := range options {
		if o.ArrivalAt.IsZero() {
			continue
		}
		if o.ArrivalAt.After(deadline) {
			continue
		}
		o.MarginMin = int(deadline.Sub(o.ArrivalAt).Minutes())
		inWindow = append(inWindow, o)
	}

	if len(inWindow) == 0 {
		add("help", fmt.Sprintf("ни один маршрут не приходит в «%s» до %s", dest, deadline.Format("15:04")))
		return &Result{
			Status:      event.StatusNeedsHelp,
			Decisions:   log,
			RiskReasons: helpReasons(softFails),
		}, nil
	}

	rankOptions(inWindow, g.Profile, ev.BudgetPerP)
	chosen := inWindow[0]
	pinnedLost := false

	// The guest picked an option themselves: keep it chosen while the live
	// inventory still sells it; when it is gone, fall back to the best rank and
	// say so honestly.
	if g.PinnedKey != "" {
		if pinned := findByKey(inWindow, g.PinnedKey); pinned != nil {
			chosen = *pinned
			add("pinned", fmt.Sprintf("держу вариант, выбранный гостем: %s %s", chosen.ModeHuman, chosen.Number))
		} else {
			pinnedLost = true
			add("collapsed", "вариант, выбранный гостем, пропал из продажи — пересобираю на лучший доступный")
		}
	}
	alts := pickAlternatives(inWindow, chosen)

	status := event.StatusAssembled
	var risks []string

	if ev.BudgetPerP > 0 && chosen.Price > float64(ev.BudgetPerP)*float64(g.Party()) {
		status = event.StatusRisk
		risks = append(risks, fmt.Sprintf("дороже бюджета: %.0f₽ на %d чел.", chosen.Price, g.Party()))
		add("risk", risks[len(risks)-1])
	}
	if chosen.MarginMin < riskMarginMin {
		status = event.StatusRisk
		r := fmt.Sprintf("прибывает впритык — запас %d мин до сбора", chosen.MarginMin)
		risks = append(risks, r)
		add("risk", r)
	}
	if chosen.Complex {
		add("plan", "маршрут с пересадкой — помечен «сложный»")
		if status == event.StatusAssembled {
			risks = append(risks, "сложный маршрут (пересадка)")
		}
	}

	add("planned", fmt.Sprintf("%s %s→%s, прибытие %s, запас %d мин, %.0f₽",
		chosen.ModeHuman, shortStation(chosen.FromStation), shortStation(chosen.ToStation),
		chosen.ArrivalAt.Format("15:04"), chosen.MarginMin, chosen.Price))

	if pinnedLost && status == event.StatusAssembled {
		status = event.StatusReassembled
	}

	return &Result{
		Chosen:       &chosen,
		Alternatives: alts,
		Status:       status,
		RiskReasons:  risks,
		Decisions:    log,
		PinnedLost:   pinnedLost,
	}, nil
}

func findByKey(opts []event.RouteOption, key string) *event.RouteOption {
	for i := range opts {
		if opts[i].Key == key {
			return &opts[i]
		}
	}
	return nil
}

// toOption converts a raw variant into a human-facing route option.
func toOption(v tutu.Variant, deadline time.Time, eventDate string) event.RouteOption {
	dep := v.Departure()
	arr := v.Arrival()
	number, carrier := "", ""
	if len(v.Legs) > 0 && len(v.Legs[0].Segments) > 0 {
		number = v.Legs[0].Segments[0].Number()
		carrier = v.Legs[0].Segments[0].Carrier
	}
	dur := v.DurationMin
	if dur == 0 && !dep.IsZero() && !arr.IsZero() {
		dur = int(arr.Sub(dep).Minutes())
	}
	nightBefore := !dep.IsZero() && !arr.IsZero() && dep.Day() != arr.Day()
	opt := event.RouteOption{
		Mode:        v.Transport,
		ModeHuman:   v.Transport.Human(),
		FromStation: v.FromStation(),
		ToStation:   v.ToStation(),
		DepartureAt: dep,
		ArrivalAt:   arr,
		DurationMin: dur,
		Price:       v.Price.Amount,
		Currency:    v.Price.Currency,
		Transfers:   v.Transfers(),
		Number:      number,
		Carrier:     carrier,
		Complex:     v.Transfers() > 0,
		NightBefore: nightBefore,
		CheckoutURL: v.CheckoutURL,
		CheckoutRef: v.CheckoutRef,
		DetailsRef:  v.DetailsRef,
		Key:         event.OptionKey(v.Transport, number, dep),
	}
	return opt
}

// interchangeToOption renders a two-train transfer plan as a complex route
// option, honestly flagged. It has no single ticket, so it carries the per-leg
// checkout URLs and a "via" note instead of one checkout_ref.
func interchangeToOption(p tutu.InterchangePlan) event.RouteOption {
	from, to, number := "", "", ""
	var legLinks []string
	if len(p.Legs) > 0 {
		from = p.Legs[0].From
		to = p.Legs[len(p.Legs)-1].To
		nums := make([]string, 0, len(p.Legs))
		for _, l := range p.Legs {
			nums = append(nums, l.TrainNumber)
			if l.CheckoutURL != "" {
				legLinks = append(legLinks, l.CheckoutURL)
			}
		}
		number = joinStr(nums, "+")
	}
	dep := p.DepartureAt.Time
	arr := p.ArrivalAt.Time
	nightBefore := !dep.IsZero() && !arr.IsZero() && dep.Day() != arr.Day()
	firstLink := ""
	if len(legLinks) > 0 {
		firstLink = legLinks[0]
	}
	return event.RouteOption{
		Mode:        tutu.ModeRail,
		ModeHuman:   "поезд с пересадкой",
		FromStation: from,
		ToStation:   to,
		DepartureAt: dep,
		ArrivalAt:   arr,
		DurationMin: p.DurationMin,
		Price:       p.PriceFrom.Amount,
		Currency:    p.PriceFrom.Currency,
		Transfers:   p.TransferCount,
		Number:      number,
		Complex:     true,
		Via:         joinStr(p.Via, ", "),
		LegLinks:    legLinks,
		CheckoutURL: firstLink, // opens the first leg; card explains leg-by-leg
		NightBefore: nightBefore,
		Key:         event.OptionKey(tutu.ModeRail, number, dep),
	}
}

func joinStr(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func addOption(m map[string]event.RouteOption, o event.RouteOption) {
	if o.ArrivalAt.IsZero() && o.DepartureAt.IsZero() {
		return
	}
	key := fmt.Sprintf("%s|%s|%s", o.Mode, o.Number, o.DepartureAt.Format(time.RFC3339))
	if _, exists := m[key]; !exists {
		m[key] = o
	}
}

// rankOptions orders by profile: cheaper=price asc, faster=duration asc; both
// break ties by larger margin. A budget, when set, sinks over-budget options.
func rankOptions(opts []event.RouteOption, profile event.Profile, budget int) {
	overBudget := func(o event.RouteOption) bool { return budget > 0 && o.Price > float64(budget) }
	sort.SliceStable(opts, func(i, j int) bool {
		a, b := opts[i], opts[j]
		if ob := overBudget(a); ob != overBudget(b) {
			return !ob // in-budget first
		}
		if profile == event.ProfileFaster {
			if a.DurationMin != b.DurationMin {
				return a.DurationMin < b.DurationMin
			}
			return a.Price < b.Price
		}
		if a.Price != b.Price {
			return a.Price < b.Price
		}
		return a.DurationMin < b.DurationMin
	})
}

// pickAlternatives returns up to 3 options that differ from the chosen one,
// preferring mode diversity so the organizer sees genuinely different plans.
func pickAlternatives(opts []event.RouteOption, chosen event.RouteOption) []event.RouteOption {
	var alts []event.RouteOption
	seenModes := map[tutu.Mode]bool{chosen.Mode: true}
	// First, one of each other mode.
	for _, o := range opts {
		if len(alts) >= 3 {
			break
		}
		if o.Number == chosen.Number && o.Mode == chosen.Mode {
			continue
		}
		if !seenModes[o.Mode] {
			alts = append(alts, o)
			seenModes[o.Mode] = true
		}
	}
	// Then fill with same-mode alternatives.
	for _, o := range opts {
		if len(alts) >= 3 {
			break
		}
		if o.Number == chosen.Number && o.Mode == chosen.Mode {
			continue
		}
		dup := false
		for _, a := range alts {
			if a.Number == o.Number && a.Mode == o.Mode {
				dup = true
				break
			}
		}
		if !dup {
			alts = append(alts, o)
		}
	}
	return alts
}

func helpReasons(softFails []string) []string {
	if len(softFails) == 0 {
		return []string{"нет маршрута к дедлайну"}
	}
	return []string{fmt.Sprintf("недоступны режимы: %v — нет маршрута к дедлайну", softFails)}
}

func prevDate(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, -1).Format("2006-01-02")
}

func mustDay(date string) time.Time {
	d, _ := time.Parse("2006-01-02", date)
	return d
}

func sameDay(t, day time.Time) bool {
	return t.Year() == day.Year() && t.YearDay() == day.YearDay()
}

func shortStation(s string) string {
	// Trim the "City — Station (CODE)" tail to the city for compact logs.
	for _, sep := range []string{" — ", ","} {
		if i := indexOf(s, sep); i > 0 {
			return s[:i]
		}
	}
	return s
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
