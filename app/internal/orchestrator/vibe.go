package orchestrator

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
)

// RankVibeCities scores each candidate destination by the full cost of
// gathering everyone there: it prices every guest → city and keeps only
// arrivals that make the deadline. The industry sells "куда поехать"; this
// computes "где встретиться". Result is sorted by reachability, then total cost.
func (o *Orchestrator) RankVibeCities(ctx context.Context, ev event.Event, cities []string) []event.VibeCity {
	deadline, ok := ev.DeadlineTime()
	if !ok {
		return nil
	}
	if len(cities) > 8 {
		cities = cities[:8]
	}

	type cell struct {
		price     float64
		reachable bool
		arrival   time.Time
		mode      string
	}

	out := make([]event.VibeCity, len(cities))
	sem := make(chan struct{}, o.maxConc)
	var wg sync.WaitGroup

	for ci, city := range cities {
		wg.Add(1)
		go func(ci int, city string) {
			defer wg.Done()

			cells := make([]cell, len(ev.Guests))
			var inner sync.WaitGroup
			for gi, g := range ev.Guests {
				inner.Add(1)
				go func(gi int, g event.Guest) {
					defer inner.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					mr, err := o.svc.SearchMulti(ctx, g.City, city, ev.Date, g.Party(), g.Profile.Optimize(), nil, false)
					if err != nil {
						return
					}
					best := -1.0
					var bestArr time.Time
					bestMode := ""
					for _, v := range mr.Variants {
						arr := v.Arrival()
						if arr.IsZero() || arr.After(deadline) {
							continue
						}
						// Нулевая цена = «неизвестно»: известная цена всегда лучше неизвестной.
						if best < 0 || (v.Price.Amount > 0 && (best <= 0 || v.Price.Amount < best)) {
							best = v.Price.Amount
							bestArr = arr
							bestMode = v.Transport.Human()
						}
					}
					if best >= 0 {
						cells[gi] = cell{price: best, reachable: true, arrival: bestArr, mode: bestMode}
					}
				}(gi, g)
			}
			inner.Wait()

			vc := event.VibeCity{City: city}
			var latest time.Time
			for gi, c := range cells {
				leg := event.GuestLeg{Guest: ev.Guests[gi].Name, From: ev.Guests[gi].City, Mode: c.mode}
				if c.reachable {
					vc.Reachable++
					vc.TotalPrice += c.price
					leg.Price = c.price
					leg.Arrival = c.arrival.Format("15:04")
					if c.arrival.After(latest) {
						latest = c.arrival
					}
				}
				vc.Breakdown = append(vc.Breakdown, leg)
			}
			if !latest.IsZero() {
				vc.MaxArrival = latest.Format("15:04")
			}
			if vc.Reachable < len(ev.Guests) {
				vc.Note = "не все гости успевают к дедлайну"
			}
			out[ci] = vc
		}(ci, city)
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Reachable != out[j].Reachable {
			return out[i].Reachable > out[j].Reachable
		}
		return out[i].TotalPrice < out[j].TotalPrice
	})
	return out
}
