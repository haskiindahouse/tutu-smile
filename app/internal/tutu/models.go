// Package tutu is the typed domain layer over the raw Tutu MCP payloads.
// It parses only the fields the planner needs and preserves each offer's
// checkout_ref verbatim (the server insists the checkout link is built from it
// byte-for-byte, never reassembled).
package tutu

import (
	"encoding/json"
	"time"
)

// Mode is a transport mode as the MCP names it.
type Mode string

const (
	ModeAvia    Mode = "avia"
	ModeRail    Mode = "railway"
	ModeBus     Mode = "bus"
	ModeEtrain  Mode = "etrain"
	ModeUnknown Mode = "unknown"
)

// Human returns a short Russian label for a mode.
func (m Mode) Human() string {
	switch m {
	case ModeAvia:
		return "самолёт"
	case ModeRail:
		return "поезд"
	case ModeBus:
		return "автобус"
	case ModeEtrain:
		return "электричка"
	default:
		return string(m)
	}
}

type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// ISOTime tolerates the server's RFC3339 strings with offsets and empty values.
type ISOTime struct{ time.Time }

func (t *ISOTime) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" || s == `""` {
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	if str == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, str)
	if err != nil {
		// Some payloads omit the colon in the zone; try a couple of layouts.
		for _, layout := range []string{"2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05"} {
			if p, e := time.Parse(layout, str); e == nil {
				t.Time = p
				return nil
			}
		}
		return err
	}
	t.Time = parsed
	return nil
}

type ReviewSummary struct {
	Rating      float64 `json:"rating"`
	ReviewCount int     `json:"review_count"`
	Label       string  `json:"label"`
	Subject     string  `json:"subject"`
}

type Segment struct {
	From        string         `json:"from"`
	To          string         `json:"to"`
	DepartureAt ISOTime        `json:"departure_at"`
	ArrivalAt   ISOTime        `json:"arrival_at"`
	DurationMin int            `json:"duration_min"`
	Carrier     string         `json:"carrier"`
	VoyageNo    string         `json:"voyage_no"`
	TrainNumber string         `json:"train_number"`
	VehicleMeta map[string]any `json:"vehicle_meta"`
	Review      *ReviewSummary `json:"review_summary"`
}

// Number returns the human vehicle identifier (flight or train number).
func (s Segment) Number() string {
	if s.TrainNumber != "" {
		return s.TrainNumber
	}
	return s.VoyageNo
}

type Leg struct {
	Label       string    `json:"label"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	DepartureAt ISOTime   `json:"departure_at"`
	ArrivalAt   ISOTime   `json:"arrival_at"`
	DurationMin int       `json:"duration_min"`
	Segments    []Segment `json:"segments"`
}

// Variant is one bookable travel option (avia/rail/bus/etrain).
type Variant struct {
	OfferID       string          `json:"offer_id"`
	Transport     Mode            `json:"transport"`
	Price         Money           `json:"price"`
	DurationMin   int             `json:"duration_min"`
	Carriers      []string        `json:"carriers"`
	SearchURL     string          `json:"search_results_url"`
	SegmentsCount int             `json:"segments_count"`
	DepartureAt   ISOTime         `json:"departure_at"`
	ArrivalAt     ISOTime         `json:"arrival_at"`
	Legs          []Leg           `json:"legs"`
	CheckoutURL   string          `json:"checkout_url"`
	CheckoutRef   json.RawMessage `json:"checkout_ref"`
	DetailsRef    json.RawMessage `json:"details_ref"` // rail/bus: opens seatmap & details
	Review        *ReviewSummary  `json:"review_summary"`
}

// Departure returns the best available departure time (top-level or first leg).
func (v Variant) Departure() time.Time {
	if !v.DepartureAt.IsZero() {
		return v.DepartureAt.Time
	}
	if len(v.Legs) > 0 {
		return v.Legs[0].DepartureAt.Time
	}
	return time.Time{}
}

// Arrival returns the best available arrival time.
func (v Variant) Arrival() time.Time {
	if !v.ArrivalAt.IsZero() {
		return v.ArrivalAt.Time
	}
	if len(v.Legs) > 0 {
		return v.Legs[len(v.Legs)-1].ArrivalAt.Time
	}
	return time.Time{}
}

// Transfers is the number of changes on this variant (segments - 1, floored).
func (v Variant) Transfers() int {
	n := 0
	for _, l := range v.Legs {
		n += len(l.Segments)
	}
	if n <= 1 {
		return 0
	}
	return n - 1
}

// FromStation / ToStation return the real endpoint names for the whole trip.
func (v Variant) FromStation() string {
	if len(v.Legs) > 0 && len(v.Legs[0].Segments) > 0 {
		return v.Legs[0].Segments[0].From
	}
	return ""
}

func (v Variant) ToStation() string {
	if n := len(v.Legs); n > 0 {
		last := v.Legs[n-1]
		if m := len(last.Segments); m > 0 {
			return last.Segments[m-1].To
		}
	}
	return ""
}

// ModeStat is one row of meta.modes_summary.
type ModeStat struct {
	Count       int               `json:"count"`
	MinPrice    *float64          `json:"min_price"`
	MinDuration *int              `json:"min_duration_min"`
	Interchange []InterchangePlan `json:"interchange_routes"`
}

// MultiResult is the parsed search_multitransport response.
type MultiResult struct {
	Variants     []Variant           `json:"variants"`
	ModesSummary map[string]ModeStat `json:"-"`
	Unavailable  []string            `json:"-"`
	Meta         json.RawMessage     `json:"meta"`
}

// RailResult is the parsed search_rail response (offers share the Variant shape).
type RailResult struct {
	Offers      []Variant         `json:"offers"`
	Interchange []InterchangePlan `json:"-"`
	Meta        json.RawMessage   `json:"meta"`
}

// InterchangePlan is a two-train transfer suggestion (no single ticket): the
// server returns these when no direct train runs the route. The product honours
// the ТЗ rule "честно помечаются «сложный маршрут»" — they are shown as complex
// options booked leg by leg.
type InterchangePlan struct {
	Via           []string              `json:"via"`
	TransferCount int                   `json:"transfer_count"`
	DepartureAt   ISOTime               `json:"departure_at"`
	ArrivalAt     ISOTime               `json:"arrival_at"`
	DurationMin   int                   `json:"duration_min"`
	PriceFrom     Money                 `json:"price_from"`
	Legs          []InterchangeLeg      `json:"legs"`
	Transfers     []InterchangeTransfer `json:"transfers"`
}

type InterchangeLeg struct {
	TrainNumber string  `json:"train_number"`
	From        string  `json:"from"`
	To          string  `json:"to"`
	DepartureAt ISOTime `json:"departure_at"`
	ArrivalAt   ISOTime `json:"arrival_at"`
	PriceFrom   Money   `json:"price_from"`
	CheckoutURL string  `json:"checkout_url"`
}

type InterchangeTransfer struct {
	LayoverMin     int  `json:"layover_min"`
	ChangesStation bool `json:"changes_station"`
}

// CheckoutLink is the create_checkout_link result.
type CheckoutLink struct {
	Kind        string `json:"kind"`
	CheckoutURL string `json:"checkout_url"`
	Note        string `json:"purchase_url_note"`
}
