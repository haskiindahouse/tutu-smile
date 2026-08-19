package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/companions"
	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/llm"
)

// --- Smart intake: freeform text / voice transcript / pasted chat → draft ---

type parseReq struct {
	Text  string          `json:"text"`
	Draft *llm.EventDraft `json:"draft"` // present when amending an existing draft
}

func (s *Server) handleParse(w http.ResponseWriter, r *http.Request) {
	var req parseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "пустой текст")
		return
	}
	if !s.llm.Enabled() {
		writeErr(w, http.StatusServiceUnavailable,
			"умный ввод требует ключ OpenRouter (OPENROUTER_API_KEY в .env) — заполните форму вручную")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	draft, err := s.llm.ParseEvent(ctx, req.Text, req.Draft, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "не смог разобрать: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft})
}

// --- Guest agency: the shared card is a product surface, not a printout ---

// findRow locates a guest's row in a board copy. Returns nil when the board
// is not ready or the guest is unknown.
func findRow(b *event.Board, gid string) *event.BoardRow {
	if b == nil {
		return nil
	}
	for i := range b.Rows {
		if b.Rows[i].GuestID == gid {
			return &b.Rows[i]
		}
	}
	return nil
}

// cloneBoard deep-copies via JSON so handlers never mutate the shared board
// in place (SSE subscribers hold references to it). Server-side ссылки опций
// (CheckoutRef/DetailsRef, json:"-") round-trip стирает — восстанавливаем их
// по стабильному RouteOption.Key, иначе строка теряет checkout/seatmap-ссылки
// до следующего ребилда.
func cloneBoard(b *event.Board) (*event.Board, error) {
	if b == nil {
		return nil, nil
	}
	buf, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var nb event.Board
	if err := json.Unmarshal(buf, &nb); err != nil {
		return nil, err
	}
	type refs struct{ checkout, details json.RawMessage }
	byKey := map[string]refs{}
	collect := func(o *event.RouteOption) {
		if o != nil && (o.CheckoutRef != nil || o.DetailsRef != nil) {
			byKey[o.Key] = refs{o.CheckoutRef, o.DetailsRef}
		}
	}
	for i := range b.Rows {
		collect(b.Rows[i].Chosen)
		for j := range b.Rows[i].Alternatives {
			collect(&b.Rows[i].Alternatives[j])
		}
	}
	restore := func(o *event.RouteOption) {
		if o == nil {
			return
		}
		if r, ok := byKey[o.Key]; ok {
			o.CheckoutRef, o.DetailsRef = r.checkout, r.details
		}
	}
	for i := range nb.Rows {
		restore(nb.Rows[i].Chosen)
		for j := range nb.Rows[i].Alternatives {
			restore(&nb.Rows[i].Alternatives[j])
		}
	}
	return &nb, nil
}

// handleGuestChoose pins the option the guest picked. The board is updated in
// place for instant feedback; the pin itself lives on the guest, so every
// future rebuild honours it while the option survives in live inventory.
func (s *Server) handleGuestChoose(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	gid := r.PathValue("gid")
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeErr(w, http.StatusBadRequest, "key required")
		return
	}

	nb, err := cloneBoard(st.CurrentBoard())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "board clone failed")
		return
	}
	row := findRow(nb, gid)
	if row == nil {
		writeErr(w, http.StatusNotFound, "guest not found on board")
		return
	}
	if row.Purchased {
		writeErr(w, http.StatusConflict, "билет уже куплен — маршрут закреплён")
		return
	}

	// Locate the option among chosen + alternatives.
	var picked *event.RouteOption
	if row.Chosen != nil && row.Chosen.Key == body.Key {
		picked = row.Chosen
	} else {
		for i := range row.Alternatives {
			if row.Alternatives[i].Key == body.Key {
				// Swap: previous chosen goes back to the pool.
				alt := row.Alternatives[i]
				if row.Chosen != nil {
					row.Alternatives[i] = *row.Chosen
				} else {
					row.Alternatives = append(row.Alternatives[:i], row.Alternatives[i+1:]...)
				}
				row.Chosen = &alt
				picked = row.Chosen
				break
			}
		}
	}
	if picked == nil {
		writeErr(w, http.StatusBadRequest, "такого варианта уже нет — обновите карточку")
		return
	}

	st.UpdateEvent(func(ev *event.Event) {
		for i := range ev.Guests {
			if ev.Guests[i].ID == gid {
				ev.Guests[i].PinnedKey = body.Key
			}
		}
	})

	row.Pinned = true
	if row.Status == event.StatusWaiting || row.Status == event.StatusReassembled {
		row.Status = event.StatusAssembled
	}
	row.Decisions = append(row.Decisions, event.DecisionEntry{
		At: time.Now(), Kind: "pinned",
		Detail: fmt.Sprintf("гость сам выбрал «%s %s», прибытие %s — держу этот вариант", picked.ModeHuman, picked.Number, timeHHMM(picked.ArrivalAt)),
	})
	nb.UpdatedAt = time.Now()
	st.SetBoard(nb)

	// The checkout link of a newly promoted alternative may be missing; a
	// background rebuild resolves it and re-runs the wave around the pin.
	s.kickRebuild(st)
	writeJSON(w, http.StatusOK, map[string]any{"status": "pinned", "row": row})
}

// handleGuestPurchased freezes/unfreezes a row after the guest confirms the
// purchase. Purchased rows are exempt from re-planning, wave swaps and risk
// reclassification.
func (s *Server) handleGuestPurchased(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	gid := r.PathValue("gid")
	var body struct {
		Purchased bool `json:"purchased"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	nb, err := cloneBoard(st.CurrentBoard())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "board clone failed")
		return
	}
	row := findRow(nb, gid)
	if row == nil {
		writeErr(w, http.StatusNotFound, "guest not found on board")
		return
	}
	if body.Purchased && row.Chosen == nil {
		writeErr(w, http.StatusBadRequest, "нечего подтверждать — маршрут ещё не собран")
		return
	}

	st.UpdateEvent(func(ev *event.Event) {
		for i := range ev.Guests {
			if ev.Guests[i].ID == gid {
				ev.Guests[i].Purchased = body.Purchased
				if body.Purchased && row.Chosen != nil {
					// The bought option is by definition the guest's pick.
					ev.Guests[i].PinnedKey = row.Chosen.Key
				}
			}
		}
	})

	wasPurchased := row.Purchased
	row.Purchased = body.Purchased
	if body.Purchased {
		row.Status = event.StatusPurchased
		row.Decisions = append(row.Decisions, event.DecisionEntry{
			At: time.Now(), Kind: "purchased",
			Detail: "гость подтвердил покупку билета — строка закреплена",
		})
	} else if wasPurchased {
		// Only a genuinely frozen row unfreezes; on any other row
		// {"purchased": false} is a noop, not a fake «снял отметку» + rebuild.
		row.Status = event.StatusAssembled
		row.Decisions = append(row.Decisions, event.DecisionEntry{
			At: time.Now(), Kind: "purchased",
			Detail: "гость снял отметку о покупке — возвращаю строку в живую перепроверку",
		})
		s.kickRebuild(st)
	}
	nb.UpdatedAt = time.Now()
	st.SetBoard(nb)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "row": row})
}

// handleGuestConsent records the guest's agreement to reveal their name to
// companions. Names unlock only when both sides of a match consent.
func (s *Server) handleGuestConsent(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	gid := r.PathValue("gid")
	var body struct {
		Consent bool `json:"consent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	st.UpdateEvent(func(ev *event.Event) {
		for i := range ev.Guests {
			if ev.Guests[i].ID == gid {
				ev.Guests[i].CompanionConsent = body.Consent
			}
		}
	})

	// Recompute companion matches on the current rows so mutual consent shows
	// up immediately, preserving the seat hints already fetched.
	nb, err := cloneBoard(st.CurrentBoard())
	if err == nil && nb != nil {
		hints := map[string]string{}
		for _, c := range nb.Companions {
			hints[c.GuestA+"|"+c.GuestB] = c.SeatHint
		}
		nb.Companions = companions.Find(st.Snapshot(), nb.Rows)
		for i := range nb.Companions {
			c := &nb.Companions[i]
			if h, ok := hints[c.GuestA+"|"+c.GuestB]; ok {
				c.SeatHint = h
			}
		}
		nb.UpdatedAt = time.Now()
		st.SetBoard(nb)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "consent": body.Consent})
}

// kickRebuild schedules a full background rebuild (fresh searches) so a pin
// or unfreeze settles into a consistent board.
func (s *Server) kickRebuild(st *event.State) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s.mgr.BuildAndStore(ctx, st, false)
	}()
}

// --- ICS export: the gather instant in every guest's calendar ---

func (s *Server) handleICS(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	ev := st.Snapshot()
	gather, ok := ev.GatherTime()
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad event date")
		return
	}
	start := gather.UTC().Format("20060102T150405Z")
	end := gather.Add(3 * time.Hour).UTC().Format("20060102T150405Z")
	stamp := time.Now().UTC().Format("20060102T150405Z")
	name := icsEscape(ev.Name)
	if name == "" {
		name = "Событие"
	}
	var b strings.Builder
	for _, line := range []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//ulybka//smile//RU",
		"BEGIN:VEVENT",
		"UID:" + ev.ID + "@ulybka",
		"DTSTAMP:" + stamp,
		"DTSTART:" + start,
		"DTEND:" + end,
		"SUMMARY:" + name + " — все в сборе",
		"LOCATION:" + icsEscape(ev.Destination),
		"DESCRIPTION:Сбор гостей к " + ev.Deadline + ". Табло: улыбка/#/e/" + ev.ID,
		"END:VEVENT",
		"END:VCALENDAR",
	} {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ulybka-`+ev.ID+`.ics"`)
	w.Write([]byte(b.String()))
}

func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
