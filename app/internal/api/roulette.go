package api

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

// «Рулетка: куда ехать» — лекарство от паралича выбора. Пользователь задаёт
// интересы, бюджет и даты; сервер собирает ЖИВУЮ полную цену поездки
// (туда + обратно + отель на все ночи) по городам, подходящим под интересы,
// и отдаёт варианты — фронт крутит их как барабан и «выбрасывает» город.
// Случайность честная: разыгрываются только варианты, влезающие в бюджет.

type rouletteReq struct {
	Origin    string   `json:"origin"`
	Date      string   `json:"date"` // YYYY-MM-DD, день выезда
	Days      int      `json:"days"` // длительность поездки в днях
	Budget    int      `json:"budget"`
	Interests []string `json:"interests"`
	Scope     string   `json:"scope"`     // rf | abroad | any
	VisaFree  bool     `json:"visa_free"` // только безвиз для граждан РФ
	// ExcludeModes убирает транспорт целиком: avia | railway | bus
	// («без поездов» выключает и электрички).
	ExcludeModes []string `json:"exclude_modes"`
	// City: конкретный город для /api/roulette/price — колесо уже упало,
	// теперь ищется маршрут ТОЛЬКО туда.
	City string `json:"city"`
	// Backups: запасные города на случай, если у выпавшего нет живой дороги —
	// считаются параллельно, колесо «доворачивается» на первый живой.
	Backups []string `json:"backups"`
	// Cities pins the drum: the pool the frontend is ALREADY spinning over
	// (from /api/roulette/pool) — pricing must run on the same список.
	Cities []string `json:"cities"`
}

// allowedModes translates exclusions into the multitransport modes list.
// nil = все режимы (параметр в запрос не кладётся).
func allowedModes(exclude []string) []tutu.Mode {
	if len(exclude) == 0 {
		return nil
	}
	off := map[string]bool{}
	for _, e := range exclude {
		off[e] = true
	}
	var out []tutu.Mode
	if !off["avia"] {
		out = append(out, tutu.ModeAvia)
	}
	if !off["railway"] {
		out = append(out, tutu.ModeRail, tutu.ModeEtrain)
	}
	if !off["bus"] {
		out = append(out, tutu.ModeBus)
	}
	return out
}

// tripLeg is one transport direction with its checkout handoff.
type tripLeg struct {
	Mode        string  `json:"mode"`
	ModeHuman   string  `json:"mode_human"`
	Number      string  `json:"number"`
	DepartureAt string  `json:"departure_at"`
	ArrivalAt   string  `json:"arrival_at"`
	Price       float64 `json:"price"`
	CheckoutURL string  `json:"checkout_url,omitempty"`
}

type tripOption struct {
	City     string           `json:"city"`
	Tags     []string         `json:"tags"`
	There    *tripLeg         `json:"there"`
	Back     *tripLeg         `json:"back"`
	Hotel    *tutu.HotelOffer `json:"hotel,omitempty"`
	Nights   int              `json:"nights"`
	Total    float64          `json:"total"`
	InBudget bool             `json:"in_budget"`
	// Via + feeder legs: the origin is a small town relayed through a hub —
	// «сначала электричка до Москвы», honestly priced into the total.
	Via        string   `json:"via,omitempty"`
	Feeder     *tripLeg `json:"feeder,omitempty"`
	FeederBack *tripLeg `json:"feeder_back,omitempty"`
	// Visa situation for RU citizens (abroad cities only).
	Abroad   bool `json:"abroad"`
	VisaFree bool `json:"visa_free"`
}

const rouletteCandidates = 8 // обойма батч-режима: 8 городов × 3 живых запроса

// candidatePool builds the drum: the FULL matching pool, shuffled — the
// wheel decides, so вариативность is the whole table, not an обойма.
func candidatePool(interests []tutu.Interest, scope tutu.Scope, origin string, visaFree bool) []string {
	pool := tutu.CitiesByInterests(interests, scope, origin, visaFree)
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool
}

func parseInterests(raw []string) []tutu.Interest {
	out := make([]tutu.Interest, 0, len(raw))
	for _, i := range raw {
		out = append(out, tutu.Interest(i))
	}
	return out
}

func parseScope(raw string) tutu.Scope {
	switch raw {
	case "rf":
		return tutu.ScopeRF
	case "abroad":
		return tutu.ScopeAbroad
	}
	return tutu.ScopeAny
}

// handleRoulettePool answers instantly (no MCP calls): the drum starts
// spinning over REAL candidate cities while prices are computed in a second,
// slower request.
func (s *Server) handleRoulettePool(w http.ResponseWriter, r *http.Request) {
	var req rouletteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	pool := candidatePool(parseInterests(req.Interests), parseScope(req.Scope), req.Origin, req.VisaFree)
	if len(pool) == 0 {
		writeErr(w, http.StatusBadRequest, "под эти фильтры городов не нашлось — снимите пару плиток или галочку безвиза")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cities": pool})
}

// priceCityWithRelay assembles the live trip to one city, falling back to a
// feeder leg through the nearest hub when the origin has no direct service.
func (s *Server) priceCityWithRelay(ctx context.Context, origin, city, date, returnDate string, days int, modes []tutu.Mode) *tripOption {
	o := s.priceTrip(ctx, origin, city, date, returnDate, days, modes)
	if o != nil && o.There != nil && o.Back != nil {
		return o
	}
	if tutu.IsHub(origin) {
		return nil
	}
	hub := tutu.NearestHub(origin)
	if strings.EqualFold(hub, city) {
		return nil
	}
	var feeder, feederBack *tripLeg
	var fwg sync.WaitGroup
	fwg.Add(2)
	go func() { defer fwg.Done(); feeder = s.cheapestLeg(ctx, origin, hub, date, nil) }()
	go func() { defer fwg.Done(); feederBack = s.cheapestLeg(ctx, hub, origin, returnDate, nil) }()
	fwg.Wait()
	if feeder == nil || feederBack == nil {
		return nil
	}
	ho := s.priceTrip(ctx, hub, city, date, returnDate, days, modes)
	if ho == nil || ho.There == nil || ho.Back == nil {
		return nil
	}
	ho.Via = hub
	ho.Feeder, ho.FeederBack = feeder, feederBack
	ho.Total += feeder.Price + feederBack.Price
	return ho
}

// handleRoulettePrice: колесо УЖЕ упало на город — собираем живую поездку.
// Запасные города считаются ПАРАЛЛЕЛЬНО: если у выпавшего дороги нет,
// колесо доворачивается на первый живой запасной тем же ответом — без
// второго круга ожидания.
func (s *Server) handleRoulettePrice(w http.ResponseWriter, r *http.Request) {
	var req rouletteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.City == "" || req.Origin == "" || req.Date == "" {
		writeErr(w, http.StatusBadRequest, "нужны origin, city, date")
		return
	}
	if req.Days < 1 {
		req.Days = 2
	}
	if req.Days > 14 {
		req.Days = 14
	}
	modes := allowedModes(req.ExcludeModes)
	returnDate := addDays(req.Date, req.Days)

	// Кандидаты в порядке приоритета: выпавший + до двух запасных.
	cities := []string{req.City}
	for _, b := range req.Backups {
		if b != "" && !strings.EqualFold(b, req.City) && len(cities) < 3 {
			cities = append(cities, b)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	results := make([]*tripOption, len(cities))
	var wg sync.WaitGroup
	for i, city := range cities {
		wg.Add(1)
		go func(i int, city string) {
			defer wg.Done()
			results[i] = s.priceCityWithRelay(ctx, req.Origin, city, req.Date, returnDate, req.Days, modes)
		}(i, city)
	}
	wg.Wait()

	for i, o := range results {
		if o == nil {
			continue
		}
		o.InBudget = req.Budget <= 0 || o.Total <= float64(req.Budget)
		o.Abroad, o.VisaFree = tutu.VisaInfoRU(o.City)
		resp := map[string]any{"option": o, "return_date": returnDate, "landed": o.City}
		if i > 0 {
			resp["note"] = "в «" + req.City + "» живой дороги не нашлось — колесо довернулось"
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeErr(w, http.StatusNotFound, "ни в «"+req.City+"», ни в запасные живой дороги не нашлось — крутим дальше")
}

func (s *Server) handleRoulette(w http.ResponseWriter, r *http.Request) {
	var req rouletteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Origin = strings.TrimSpace(req.Origin)
	if req.Origin == "" || req.Date == "" {
		writeErr(w, http.StatusBadRequest, "нужны город выезда и дата")
		return
	}
	if req.Days < 1 {
		req.Days = 2
	}
	if req.Days > 14 {
		req.Days = 14
	}
	pool := req.Cities
	if len(pool) == 0 {
		pool = candidatePool(parseInterests(req.Interests), parseScope(req.Scope), req.Origin, req.VisaFree)
	}
	if len(pool) > rouletteCandidates {
		pool = pool[:rouletteCandidates]
	}
	if len(pool) == 0 {
		writeErr(w, http.StatusBadRequest, "под эти интересы городов не нашлось — снимите пару плиток")
		return
	}

	returnDate := addDays(req.Date, req.Days)
	nights := req.Days

	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()

	options := make([]*tripOption, len(pool))
	var wg sync.WaitGroup
	for i, city := range pool {
		wg.Add(1)
		go func(i int, city string) {
			defer wg.Done()
			options[i] = s.priceTrip(ctx, req.Origin, city, req.Date, returnDate, nights, allowedModes(req.ExcludeModes))
		}(i, city)
	}
	wg.Wait()

	priced := 0
	for _, o := range options {
		if o != nil && o.There != nil && o.Back != nil {
			priced++
		}
	}

	// Малый город без прямых дальних маршрутов (Дмитров, Коломна…) — не
	// «ненаход», а плечо: находим электричку/поезд до ближайшего хаба и
	// считаем поездку от него, честно вкладывая плечо в цену.
	if priced < 2 && !tutu.IsHub(req.Origin) {
		hub := tutu.NearestHub(req.Origin)
		var feeder, feederBack *tripLeg
		var fwg sync.WaitGroup
		fwg.Add(2)
		go func() { defer fwg.Done(); feeder = s.cheapestLeg(ctx, req.Origin, hub, req.Date, nil) }()
		go func() { defer fwg.Done(); feederBack = s.cheapestLeg(ctx, hub, req.Origin, returnDate, nil) }()
		fwg.Wait()
		if feeder != nil && feederBack != nil {
			var rwg sync.WaitGroup
			for i, city := range pool {
				if options[i] != nil && options[i].There != nil && options[i].Back != nil {
					continue // прямой вариант уже есть — плечо не нужно
				}
				if strings.EqualFold(city, hub) {
					continue
				}
				rwg.Add(1)
				go func(i int, city string) {
					defer rwg.Done()
					o := s.priceTrip(ctx, hub, city, req.Date, returnDate, nights, allowedModes(req.ExcludeModes))
					if o == nil || o.There == nil || o.Back == nil {
						return
					}
					o.Via = hub
					o.Feeder, o.FeederBack = feeder, feederBack
					o.Total += feeder.Price + feederBack.Price
					options[i] = o
				}(i, city)
			}
			rwg.Wait()
		}
	}

	var out []tripOption
	for _, o := range options {
		if o == nil || o.There == nil || o.Back == nil {
			continue // город без дороги в обе стороны в рулетку не попадает
		}
		o.InBudget = req.Budget <= 0 || o.Total <= float64(req.Budget)
		out = append(out, *o)
	}
	if len(out) == 0 {
		writeErr(w, http.StatusNotFound,
			"живых маршрутов не нашлось даже через хаб — попробуйте другую дату или город покрупнее")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": out, "return_date": returnDate})
}

// priceTrip assembles the full live cost of one candidate: cheapest way
// there on the start date, cheapest way back on the return date, cheapest
// hotel for the nights between. Any missing part degrades honestly.
func (s *Server) priceTrip(ctx context.Context, origin, city, date, returnDate string, nights int, modes []tutu.Mode) *tripOption {
	opt := &tripOption{City: city, Tags: tutu.TagsOf(city), Nights: nights}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		opt.There = s.cheapestLeg(ctx, origin, city, date, modes)
	}()
	go func() {
		defer wg.Done()
		opt.Back = s.cheapestLeg(ctx, city, origin, returnDate, modes)
	}()
	go func() {
		defer wg.Done()
		hctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		hotels, err := s.svc.SearchHotels(hctx, city, date, returnDate, 1, 0, 5)
		if err != nil || len(hotels) == 0 {
			return
		}
		best := hotels[0]
		for _, h := range hotels[1:] {
			if h.Price > 0 && (best.Price == 0 || h.Price < best.Price) && h.Rating >= 7 {
				best = h
			}
		}
		opt.Hotel = &best
	}()
	wg.Wait()

	if opt.There != nil {
		opt.Total += opt.There.Price
	}
	if opt.Back != nil {
		opt.Total += opt.Back.Price
	}
	if opt.Hotel != nil {
		opt.Total += opt.Hotel.Price
	}
	return opt
}

// cheapestLeg finds the cheapest live variant for one direction.
func (s *Server) cheapestLeg(ctx context.Context, from, to, date string, modes []tutu.Mode) *tripLeg {
	lctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	mr, err := s.svc.SearchMulti(lctx, from, to, date, 1, tutu.OptimizePrice, modes, false)
	if err != nil || len(mr.Variants) == 0 {
		return nil
	}
	best := mr.Variants[0]
	for _, v := range mr.Variants[1:] {
		// Price 0 means «unknown»: any known price beats it, otherwise cheapest wins.
		if v.Price.Amount > 0 && (best.Price.Amount <= 0 || v.Price.Amount < best.Price.Amount) {
			best = v
		}
	}
	leg := &tripLeg{
		Mode:      string(best.Transport),
		ModeHuman: best.Transport.Human(),
		Price:     best.Price.Amount,
	}
	if len(best.Legs) > 0 && len(best.Legs[0].Segments) > 0 {
		leg.Number = best.Legs[0].Segments[0].Number()
	}
	if d := best.Departure(); !d.IsZero() {
		leg.DepartureAt = d.Format(time.RFC3339)
	}
	if a := best.Arrival(); !a.IsZero() {
		leg.ArrivalAt = a.Format(time.RFC3339)
	}
	// Полный чейн до чекаута — рулетка выдаёт готовую к покупке поездку.
	if best.CheckoutURL != "" {
		leg.CheckoutURL = best.CheckoutURL
	} else if len(best.CheckoutRef) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if link, err := s.svc.CreateCheckoutLink(cctx, best.CheckoutRef); err == nil {
			leg.CheckoutURL = link.CheckoutURL
		}
	}
	return leg
}

func addDays(date string, n int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

// --- Маршрут по городу с экспортом в Google Карты ---

// handleSpots: лента впечатлений города (карусель на карточке результата).
func (s *Server) handleSpots(w http.ResponseWriter, r *http.Request) {
	var req cityPlanReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.City) == "" {
		writeErr(w, http.StatusBadRequest, "нужен город")
		return
	}
	human := make([]string, 0, len(req.Interests))
	for _, i := range req.Interests {
		if h, ok := tutu.InterestHuman[tutu.Interest(i)]; ok {
			human = append(human, h)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	spots, err := s.llm.Spots(ctx, req.City, human)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "лента впечатлений требует LLM: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spots": spots})
}

type cityPlanReq struct {
	City      string   `json:"city"`
	Interests []string `json:"interests"`
	Days      int      `json:"days"`
}

func (s *Server) handleCityPlan(w http.ResponseWriter, r *http.Request) {
	var req cityPlanReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.City) == "" {
		writeErr(w, http.StatusBadRequest, "нужен город")
		return
	}
	if req.Days < 1 {
		req.Days = 1
	}
	if req.Days > 7 {
		req.Days = 7
	}
	human := make([]string, 0, len(req.Interests))
	for _, i := range req.Interests {
		if h, ok := tutu.InterestHuman[tutu.Interest(i)]; ok {
			human = append(human, h)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	plan, err := s.llm.CityPlan(ctx, req.City, human, req.Days)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "план не собрался: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
