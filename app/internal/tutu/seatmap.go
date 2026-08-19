package tutu

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// SeatGroup is one "seats together" proposal from get_rail_seatmap: a concrete
// car and adjacent seat numbers two companions can actually buy.
type SeatGroup struct {
	CarNumber    string   `json:"car_number"`
	CarType      string   `json:"car_type"`
	ServiceClass string   `json:"service_class"`
	SeatNumbers  []string `json:"seat_numbers"`
	TotalPrice   float64  `json:"total_price"`
	Currency     string   `json:"currency"`
	FareType     string   `json:"fare_type"`
}

// Human renders the group as one board-ready phrase.
func (g SeatGroup) Human() string {
	seats := ""
	for i, s := range g.SeatNumbers {
		if i > 0 {
			seats += " и "
		}
		seats += s
	}
	label := carTypeRu(g.CarType)
	if g.ServiceClass != "" {
		label += " " + g.ServiceClass
	}
	return fmt.Sprintf("вагон %s (%s), места %s рядом — %.0f₽ за двоих", g.CarNumber, label, seats, g.TotalPrice)
}

// SeatsTogether asks the live seat map whether n companions can sit together
// on the offer's train. detailsRef is the offer's details_ref passed verbatim.
// A train with no layout (seatmap_status != ok) returns (nil, nil): not an
// error — seats are then picked on Tutu's own seat page.
func (s *Service) SeatsTogether(ctx context.Context, detailsRef json.RawMessage, n int) ([]SeatGroup, error) {
	if len(detailsRef) == 0 {
		return nil, nil
	}
	var ref map[string]any
	if err := json.Unmarshal(detailsRef, &ref); err != nil {
		return nil, fmt.Errorf("details_ref not an object: %w", err)
	}
	if n < 2 {
		n = 2
	}
	args := map[string]any{
		"details_ref":    ref,
		"task":           "together",
		"seats_together": n,
	}
	raw, err := s.mcp.CallTool(ctx, "get_rail_seatmap", args, false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Status string `json:"seatmap_status"`
		Groups map[string][]struct {
			CarNumber    string   `json:"car_number"`
			CarType      string   `json:"car_type"`
			ServiceClass string   `json:"service_class"`
			SeatNumbers  []string `json:"seat_numbers"`
			TotalPrice   Money    `json:"total_price"`
			FareType     string   `json:"total_fare_type"`
		} `json:"groups_by_car_type"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse seatmap: %w", err)
	}
	if res.Status != "ok" {
		return nil, nil
	}
	var out []SeatGroup
	for _, groups := range res.Groups {
		for _, g := range groups {
			out = append(out, SeatGroup{
				CarNumber:    g.CarNumber,
				CarType:      g.CarType,
				ServiceClass: g.ServiceClass,
				SeatNumbers:  g.SeatNumbers,
				TotalPrice:   g.TotalPrice.Amount,
				Currency:     g.TotalPrice.Currency,
				FareType:     g.FareType,
			})
		}
	}
	// Cheapest proposal first — that is the one the note quotes.
	sort.SliceStable(out, func(i, j int) bool { return out[i].TotalPrice < out[j].TotalPrice })
	return out, nil
}

func carTypeRu(t string) string {
	switch t {
	case "COMPARTMENT":
		return "купе"
	case "RESERVED_SEAT":
		return "плацкарт"
	case "SEDENTARY":
		return "сидячий"
	case "LUXURY", "SV":
		return "СВ"
	default:
		return t
	}
}
