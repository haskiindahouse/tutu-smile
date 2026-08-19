package tutu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// HotelOffer is one hotel search row, trimmed to what the board and the guest
// card show. Price is the WHOLE-STAY total (Tutu's price_basis=stay_total),
// never per-night — the ТЗ lists this as a known trap.
type HotelOffer struct {
	HotelID          string          `json:"hotel_id"`
	Name             string          `json:"name"`
	Stars            int             `json:"stars"`
	Rating           float64         `json:"rating"`
	ReviewCount      int             `json:"review_count"`
	Address          string          `json:"address"`
	RoomName         string          `json:"room_name"`
	Price            float64         `json:"price"`
	Currency         string          `json:"currency"`
	FreeCancellation bool            `json:"free_cancellation"`
	CheckIn          string          `json:"check_in"`
	CheckOut         string          `json:"check_out"`
	CheckoutURL      string          `json:"checkout_url"`
	CheckoutRef      json.RawMessage `json:"-"` // kept server-side for the deeplink chain
}

// hotelRow mirrors the search_hotels response row (compact view).
type hotelRow struct {
	HotelID     any     `json:"hotel_id"` // string alias or numeric id, depending on upstream
	Name        string  `json:"name"`
	Stars       int     `json:"stars"`
	Rating      float64 `json:"rating"`
	ReviewCount int     `json:"review_count"`
	Address     string  `json:"address"`
	CheckoutURL string  `json:"checkout_url"`
	BestOffer   *struct {
		RoomName         string `json:"room_name"`
		Price            Money  `json:"price"`
		FreeCancellation *bool  `json:"free_cancellation"`
		CheckoutURL      string `json:"checkout_url"`
	} `json:"best_offer"`
	CheckoutRef json.RawMessage `json:"checkout_ref"`
}

// hotelIDString normalizes the id: the MCP documents it as a string alias,
// but some payloads carry a bare number — accept both instead of failing the
// whole search on a type mismatch.
func hotelIDString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// SearchHotels finds lodging in the event city for one guest party.
// priceMax caps the PER-NIGHT price (that is what the server enforces);
// 0 = no cap. Results come back relevance-sorted by Tutu; we keep the
// server's order and just trim.
func (s *Service) SearchHotels(ctx context.Context, city, checkIn, checkOut string, adults, priceMax, limit int) ([]HotelOffer, error) {
	if limit <= 0 {
		limit = 3
	}
	args := map[string]any{
		"city_name": city,
		"check_in":  checkIn,
		"check_out": checkOut,
		"adults":    adults,
		"page_size": limit,
		"view":      "compact",
	}
	if priceMax > 0 {
		args["price_max"] = priceMax
	}
	raw, err := s.mcp.CallTool(ctx, "search_hotels", args, false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Hotels []hotelRow `json:"hotels"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse hotels: %w", err)
	}
	out := make([]HotelOffer, 0, len(res.Hotels))
	for _, h := range res.Hotels {
		o := HotelOffer{
			HotelID:     hotelIDString(h.HotelID),
			Name:        h.Name,
			Stars:       h.Stars,
			Rating:      h.Rating,
			ReviewCount: h.ReviewCount,
			Address:     h.Address,
			CheckIn:     checkIn,
			CheckOut:    checkOut,
			CheckoutURL: h.CheckoutURL,
			CheckoutRef: h.CheckoutRef,
		}
		if h.BestOffer != nil {
			o.RoomName = h.BestOffer.RoomName
			o.Price = h.BestOffer.Price.Amount
			o.Currency = h.BestOffer.Price.Currency
			if h.BestOffer.FreeCancellation != nil {
				o.FreeCancellation = *h.BestOffer.FreeCancellation
			}
			if h.BestOffer.CheckoutURL != "" {
				o.CheckoutURL = h.BestOffer.CheckoutURL
			}
		}
		out = append(out, o)
	}
	return out, nil
}
