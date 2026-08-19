package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

// «Увидеться вдвоём»: two people in two cities, no occasion needed — the
// service answers WHERE meeting is cheapest with live prices for both sides.
// The industry sells «куда поехать»; this computes «где встретиться».

// meetHubs is the no-LLM candidate set: well-connected cities between which
// most RU pairs have sane routes. The LLM narrows this list contextually when
// a key is present.
var meetHubs = []string{
	"Москва", "Санкт-Петербург", "Казань", "Нижний Новгород", "Ярославль",
	"Владимир", "Тула", "Воронеж", "Самара", "Екатеринбург", "Сочи", "Уфа",
}

type meetReq struct {
	CityA     string   `json:"city_a"`
	CityB     string   `json:"city_b"`
	Date      string   `json:"date"`
	Deadline  string   `json:"deadline"`
	Interests []string `json:"interests"` // narrows the meeting-city pool
}

func (s *Server) handleMeet(w http.ResponseWriter, r *http.Request) {
	var req meetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.CityA, req.CityB = strings.TrimSpace(req.CityA), strings.TrimSpace(req.CityB)
	if req.CityA == "" || req.CityB == "" {
		writeErr(w, http.StatusBadRequest, "нужны оба города")
		return
	}
	if req.Date == "" {
		writeErr(w, http.StatusBadRequest, "нужна дата")
		return
	}
	if req.Deadline == "" {
		req.Deadline = "15:00"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Candidates: interest tiles narrow the pool first; the LLM proposes
	// contextually when available, the hub list otherwise. Both origins are
	// excluded — the point is to MEET, not to host.
	cities := meetHubs
	note := ""
	wish := "город для встречи двух друзей примерно на полпути, чтобы обоим было удобно и недорого добраться за день"
	if len(req.Interests) > 0 {
		ints := make([]tutu.Interest, 0, len(req.Interests))
		human := make([]string, 0, len(req.Interests))
		for _, i := range req.Interests {
			ints = append(ints, tutu.Interest(i))
			if h, ok := tutu.InterestHuman[tutu.Interest(i)]; ok {
				human = append(human, h)
			}
		}
		if tagged := tutu.CitiesByInterests(ints, tutu.ScopeRF, "", false); len(tagged) > 0 {
			cities = tagged
		}
		wish += "; интересы: " + strings.Join(human, ", ")
	}
	if s.llm.Enabled() {
		if spec, err := s.llm.ExpandVibe(ctx, wish, []string{req.CityA, req.CityB}); err == nil && len(spec.Cities) > 0 {
			cities = spec.Cities
			note = spec.Note
		}
	}
	filtered := make([]string, 0, len(cities))
	for _, c := range cities {
		if !strings.EqualFold(c, req.CityA) && !strings.EqualFold(c, req.CityB) {
			filtered = append(filtered, c)
		}
	}

	tmp := event.Event{
		Date:        req.Date,
		Deadline:    req.Deadline,
		BufferHours: 1,
		Guests: []event.Guest{
			{ID: "a", Name: "Ты", City: req.CityA, Profile: event.ProfileCheaper, Adults: 1},
			{ID: "b", Name: "Друг", City: req.CityB, Profile: event.ProfileCheaper, Adults: 1},
		},
	}
	ranked := s.orch.RankVibeCities(ctx, tmp, filtered)

	// Meeting math: both must make it; among those — cheapest total smile.
	writeJSON(w, http.StatusOK, map[string]any{"cities": ranked, "note": note})
}
