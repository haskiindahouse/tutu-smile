package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/companions"
	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

func destCoord(city string) (tutu.Coord, bool) { return tutu.CityCoord(city) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	calls, hits := s.mcp.Stats()
	open, until := s.mcp.BreakerOpen()
	proxies, benched := s.mcp.ProxyStats()
	resp := map[string]any{
		"status":              "ok",
		"llm_enabled":         s.llm.Enabled(),
		"mcp_calls":           calls,
		"mcp_cache_hits":      hits,
		"mcp_breaker_open":    open,
		"mcp_proxies":         proxies,
		"mcp_proxies_benched": benched,
		"events":              len(s.store.List()),
		"time":                time.Now().Format(time.RFC3339),
	}
	if open {
		resp["mcp_breaker_until"] = until.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mapbox_token": s.cfg.MapboxToken,
		"llm_enabled":  s.llm.Enabled(),
		"recheck_sec":  int(s.cfg.RecheckEvery.Seconds()),
		"cities":       tutu.Cities(),
	})
}

// --- Vibe wizard ---

type vibeReq struct {
	Vibe        string        `json:"vibe"`
	Date        string        `json:"date"`
	Deadline    string        `json:"deadline"`
	BufferHours float64       `json:"buffer_hours"`
	Guests      []event.Guest `json:"guests"`
}

func (s *Server) handleVibe(w http.ResponseWriter, r *http.Request) {
	var req vibeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.BufferHours == 0 {
		req.BufferHours = 2
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	cities := make([]string, 0, len(req.Guests))
	for _, g := range req.Guests {
		cities = append(cities, g.City)
	}
	spec, _ := s.llm.ExpandVibe(ctx, req.Vibe, cities)

	tmp := event.Event{
		InputMode:   event.InputVibe,
		Vibe:        req.Vibe,
		Date:        req.Date,
		Deadline:    req.Deadline,
		BufferHours: req.BufferHours,
		Guests:      req.Guests,
	}
	ranked := s.orch.RankVibeCities(ctx, tmp, spec.Cities)

	writeJSON(w, http.StatusOK, map[string]any{
		"spec":   spec,
		"cities": ranked,
	})
}

// --- Events ---

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var ev event.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	applyDefaults(&ev)
	if ev.Destination == "" {
		writeErr(w, http.StatusBadRequest, "destination required")
		return
	}
	if len(ev.Guests) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one guest required")
		return
	}
	st := s.store.Create(ev)
	s.kickBuild(st)
	s.mgr.Start(st.Event.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": st.Event.ID, "event": st.Snapshot()})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"events": s.store.List()})
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"event": st.Snapshot(),
		"board": st.CurrentBoard(),
	})
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	writeJSON(w, http.StatusOK, st.CurrentBoard())
}

func (s *Server) handleRecheck(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.mgr.BuildAndStore(ctx, st, true)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rechecking"})
}

func (s *Server) handleAddGuest(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	var g event.Guest
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if g.Adults < 1 {
		g.Adults = 1
	}
	st.UpdateEvent(func(ev *event.Event) {
		if g.ID == "" {
			g.ID = fmt.Sprintf("g%d", len(ev.Guests)+1)
		}
		ev.Guests = append(ev.Guests, g)
	})
	s.kickBuild(st)
	writeJSON(w, http.StatusOK, map[string]any{"event": st.Snapshot()})
}

// handleGuestCard returns the guest-facing view: their row plus companion
// matches with the other party redacted until mutual consent.
func (s *Server) handleGuestCard(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	gid := r.PathValue("gid")
	board := st.CurrentBoard()
	if board == nil {
		writeErr(w, http.StatusAccepted, "board not ready")
		return
	}
	var row *event.BoardRow
	for i := range board.Rows {
		if board.Rows[i].GuestID == gid {
			row = &board.Rows[i]
			break
		}
	}
	if row == nil {
		writeErr(w, http.StatusNotFound, "guest not found")
		return
	}
	ev := st.Snapshot()
	var guest *event.Guest
	for i := range ev.Guests {
		if ev.Guests[i].ID == gid {
			guest = &ev.Guests[i]
			break
		}
	}
	resp := map[string]any{
		"event":      map[string]any{"id": ev.ID, "name": ev.Name, "destination": ev.Destination, "date": ev.Date, "deadline": ev.Deadline},
		"row":        row,
		"companions": companions.ForGuest(board.Companions, gid),
		"gather_at":  board.GatherAt,
	}
	if guest != nil {
		resp["guest"] = map[string]any{
			"purchased":         guest.Purchased,
			"pinned_key":        guest.PinnedKey,
			"find_companions":   guest.FindCompanions,
			"companion_consent": guest.CompanionConsent,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDemoCollapse is an explicit stage affordance: it simulates the chosen
// route of one guest dying and reassembles onto a REAL alternative from the
// last live search — so the reassembled route is genuine, only its trigger is
// manual. Never fires on its own.
func (s *Server) handleDemoCollapse(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	var body struct {
		GuestID string `json:"guest_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	board := st.CurrentBoard()
	if board == nil {
		writeErr(w, http.StatusAccepted, "board not ready")
		return
	}
	// Deep copy (с восстановлением server-side ссылок) — shared board не мутируем.
	nbp, err := cloneBoard(board)
	if err != nil || nbp == nil {
		writeErr(w, http.StatusInternalServerError, "board clone failed")
		return
	}
	nb := *nbp

	target := -1
	for i := range nb.Rows {
		if nb.Rows[i].Purchased {
			continue // a bought ticket cannot «рассыпаться» — the seat is theirs
		}
		if body.GuestID != "" && nb.Rows[i].GuestID == body.GuestID {
			target = i
			break
		}
		if body.GuestID == "" && nb.Rows[i].Chosen != nil && len(nb.Rows[i].Alternatives) > 0 && target == -1 {
			target = i
		}
	}
	if target == -1 {
		writeErr(w, http.StatusBadRequest, "no collapsible guest (need an unpurchased route with an alternative)")
		return
	}
	row := &nb.Rows[target]
	if len(row.Alternatives) == 0 || row.Chosen == nil {
		writeErr(w, http.StatusBadRequest, "guest has no alternative to reassemble onto")
		return
	}
	old := *row.Chosen
	newChosen := row.Alternatives[0]
	row.Alternatives[0] = old
	row.Chosen = &newChosen
	row.Status = event.StatusReassembled
	now := time.Now()
	row.Decisions = append(row.Decisions,
		event.DecisionEntry{At: now, Kind: "collapsed", Detail: fmt.Sprintf("маршрут «%s %s» за %.0f₽ выкупили — место кончилось", old.ModeHuman, old.Number, old.Price)},
		event.DecisionEntry{At: now, Kind: "reassembled", Detail: fmt.Sprintf("пересобрал на «%s %s» за %.0f₽, прибытие %s", newChosen.ModeHuman, newChosen.Number, newChosen.Price, timeHHMM(newChosen.ArrivalAt))},
	)
	row.LastChecked = now
	nb.UpdatedAt = now
	st.SetBoard(&nb)
	writeJSON(w, http.StatusOK, map[string]any{"status": "reassembled", "guest": row.GuestName})
}

// --- SSE stream ---

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := st.Subscribe()
	defer unsub()

	// Initial retry + first frame.
	fmt.Fprintf(w, "retry: 3000\n\n")
	flusher.Flush()

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(b)
			fmt.Fprintf(w, "event: board\ndata: %s\n\n", data)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// --- helpers ---

func (s *Server) kickBuild(st *event.State) {
	// Immediate placeholder so the board renders instantly.
	ev := st.Snapshot()
	placeholder := &event.Board{
		EventID:     ev.ID,
		Destination: ev.Destination,
		Total:       len(ev.Guests),
		UpdatedAt:   time.Now(),
	}
	if d, ok := ev.DeadlineTime(); ok {
		placeholder.Deadline = d
	}
	if g, ok := ev.GatherTime(); ok {
		placeholder.GatherAt = g
	}
	if c, ok := destCoord(ev.Destination); ok {
		placeholder.DestCoord = &c
	}
	for _, g := range ev.Guests {
		row := event.BoardRow{GuestID: g.ID, GuestName: g.Name, City: g.City, Profile: g.Profile, Status: event.StatusPlanning, NeedsLodging: g.NeedsLodging}
		if c, ok := destCoord(g.City); ok {
			row.Coord = &c
		}
		placeholder.Rows = append(placeholder.Rows, row)
	}
	st.SetBoard(placeholder)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s.mgr.BuildAndStore(ctx, st, false)
	}()
}

func applyDefaults(ev *event.Event) {
	if ev.BufferHours == 0 {
		ev.BufferHours = 2
	}
	if ev.SpacingMin == 0 {
		ev.SpacingMin = 20
	}
	if ev.InputMode == "" {
		ev.InputMode = event.InputPlace
	}
	for i := range ev.Guests {
		if ev.Guests[i].Profile == "" {
			ev.Guests[i].Profile = event.ProfileCheaper
		}
		if ev.Guests[i].Adults < 1 {
			ev.Guests[i].Adults = 1
		}
	}
}

func timeHHMM(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04")
}
