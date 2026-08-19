package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/llm"
)

// --- Join link: guests register themselves ---
//
// The organizer stops typing guests in: one join link goes to the group chat,
// every guest adds themselves in five seconds, and their row appears on the
// live board for everyone (SSE). Distributed data entry is what makes this a
// group-planning tool rather than a form.

type joinReq struct {
	Name           string `json:"name"`
	City           string `json:"city"`
	Profile        string `json:"profile"`
	Adults         int    `json:"adults"`
	Children       int    `json:"children"`
	NeedsLodging   bool   `json:"needs_lodging"`
	FindCompanions bool   `json:"find_companions"`
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	var req joinReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.City = strings.TrimSpace(req.City)
	if req.City == "" {
		writeErr(w, http.StatusBadRequest, "укажите город выезда")
		return
	}
	if req.Name == "" {
		req.Name = "Гость"
	}
	if req.Adults < 1 {
		req.Adults = 1
	}
	profile := event.ProfileCheaper
	if req.Profile == string(event.ProfileFaster) {
		profile = event.ProfileFaster
	}

	// Same person refreshing the join page must not multiply rows: an existing
	// guest with the same name+city is updated, not duplicated.
	var gid string
	st.UpdateEvent(func(ev *event.Event) {
		for i := range ev.Guests {
			g := &ev.Guests[i]
			if strings.EqualFold(g.Name, req.Name) && strings.EqualFold(g.City, req.City) {
				g.Profile = profile
				g.Adults = req.Adults
				g.Children = req.Children
				g.NeedsLodging = req.NeedsLodging
				g.FindCompanions = req.FindCompanions
				gid = g.ID
				return
			}
		}
		ng := event.Guest{
			Name: req.Name, City: req.City, Profile: profile,
			Adults: req.Adults, Children: req.Children,
			NeedsLodging: req.NeedsLodging, FindCompanions: req.FindCompanions,
			ID: fmt.Sprintf("j%d%d", len(ev.Guests)+1, time.Now().UnixNano()%1000),
		}
		ev.Guests = append(ev.Guests, ng)
		gid = ng.ID
	})

	s.kickRebuild(st)
	writeJSON(w, http.StatusOK, map[string]any{"guest_id": gid, "event_id": st.Snapshot().ID})
}

// handleJoinInfo is the public face of the join page: enough to render the
// invite, nothing private (no guest list, no prices).
func (s *Server) handleJoinInfo(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	ev := st.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        ev.Name,
		"destination": ev.Destination,
		"date":        ev.Date,
		"deadline":    ev.Deadline,
		"guests":      len(ev.Guests),
	})
}

// --- Amend: run the event with words ---
//
// «перенеси сбор на 16:00, добавь Свету из Уфы с ночёвкой» typed (or dictated)
// right on the board. The LLM merges the instruction into the current event;
// we apply frame changes and guest additions/updates. Nothing is ever deleted
// by words — removing a person stays an explicit UI action.

func (s *Server) handleAmend(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "пустая команда")
		return
	}
	if !s.llm.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "управление словами требует ключ OpenRouter")
		return
	}

	ev := st.Snapshot()
	prior := eventToDraft(ev)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	draft, err := s.llm.ParseEvent(ctx, body.Text, prior, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "не понял команду: "+err.Error())
		return
	}

	changes := s.applyDraftToEvent(st, draft)
	if len(changes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "noop", "note": "изменений не увидел", "changes": changes})
		return
	}
	s.kickRebuild(st)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "changes": changes})
}

func eventToDraft(ev event.Event) *llm.EventDraft {
	d := &llm.EventDraft{
		Name:        ev.Name,
		Destination: ev.Destination,
		Vibe:        ev.Vibe,
		Date:        ev.Date,
		Deadline:    ev.Deadline,
		BufferHours: ev.BufferHours,
		SpacingMin:  ev.SpacingMin,
		BudgetRub:   ev.BudgetPerP,
	}
	for _, g := range ev.Guests {
		d.Guests = append(d.Guests, llm.DraftGuest{
			Name: g.Name, City: g.City, Profile: string(g.Profile),
			Adults: g.Adults, Children: g.Children,
			NeedsLodging: g.NeedsLodging, FindCompanions: g.FindCompanions,
		})
	}
	return d
}

// applyDraftToEvent merges a parsed draft back into the live event and
// returns a human list of what changed (shown as a toast and in the log).
func (s *Server) applyDraftToEvent(st *event.State, d *llm.EventDraft) []string {
	var changes []string
	st.UpdateEvent(func(ev *event.Event) {
		set := func(what string, apply func()) { apply(); changes = append(changes, what) }
		if d.Name != "" && d.Name != ev.Name {
			set("название → "+d.Name, func() { ev.Name = d.Name })
		}
		if d.Destination != "" && d.Destination != ev.Destination {
			set("место → "+d.Destination, func() { ev.Destination = d.Destination })
		}
		if d.Date != "" && d.Date != ev.Date {
			set("дата → "+d.Date, func() { ev.Date = d.Date })
		}
		if d.Deadline != "" && d.Deadline != ev.Deadline {
			set("сбор → "+d.Deadline, func() { ev.Deadline = d.Deadline })
		}
		if d.BufferHours > 0 && d.BufferHours != ev.BufferHours {
			set(fmt.Sprintf("буфер → %.1f ч", d.BufferHours), func() { ev.BufferHours = d.BufferHours })
		}
		if d.SpacingMin > 0 && d.SpacingMin != ev.SpacingMin {
			set(fmt.Sprintf("зазор → %d мин", d.SpacingMin), func() { ev.SpacingMin = d.SpacingMin })
		}
		if d.BudgetRub > 0 && d.BudgetRub != ev.BudgetPerP {
			set(fmt.Sprintf("бюджет → %d₽", d.BudgetRub), func() { ev.BudgetPerP = d.BudgetRub })
		}
		for _, dg := range d.Guests {
			if dg.City == "" {
				continue
			}
			matched := false
			for i := range ev.Guests {
				g := &ev.Guests[i]
				if strings.EqualFold(g.Name, dg.Name) && strings.EqualFold(g.City, dg.City) {
					matched = true
					prof := event.ProfileCheaper
					if dg.Profile == string(event.ProfileFaster) {
						prof = event.ProfileFaster
					}
					if g.Profile != prof || g.NeedsLodging != dg.NeedsLodging ||
						g.FindCompanions != dg.FindCompanions || g.Adults != maxIntAPI(dg.Adults, 1) || g.Children != dg.Children {
						g.Profile = prof
						g.Adults = maxIntAPI(dg.Adults, 1)
						g.Children = dg.Children
						g.NeedsLodging = dg.NeedsLodging
						g.FindCompanions = dg.FindCompanions
						changes = append(changes, "обновил "+g.Name)
					}
					break
				}
			}
			if !matched {
				ng := event.Guest{
					Name: dg.Name, City: dg.City,
					Profile: event.ProfileCheaper,
					Adults:  maxIntAPI(dg.Adults, 1), Children: dg.Children,
					NeedsLodging: dg.NeedsLodging, FindCompanions: dg.FindCompanions,
					ID: fmt.Sprintf("a%d%d", len(ev.Guests)+1, time.Now().UnixNano()%1000),
				}
				if dg.Profile == string(event.ProfileFaster) {
					ng.Profile = event.ProfileFaster
				}
				if ng.Name == "" {
					ng.Name = "Гость"
				}
				ev.Guests = append(ev.Guests, ng)
				changes = append(changes, "добавил "+ng.Name+" из "+ng.City)
			}
		}
	})
	return changes
}

func maxIntAPI(a, b int) int {
	if a > b {
		return a
	}
	return b
}
