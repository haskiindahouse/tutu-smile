// Package event holds the core domain: an event (the gathering), its guests,
// and the live board rows the planner produces for each guest.
package event

import (
	"encoding/json"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/tutu"
)

// InputMode is how the organizer specified the destination.
type InputMode string

const (
	InputPlace InputMode = "place" // "Свадьба в Казани"
	InputVibe  InputMode = "vibe"  // "хотим к морю, недорого, чтобы все успели"
)

// Profile drives per-guest ranking (maps to multitransport optimize_for).
type Profile string

const (
	ProfileCheaper Profile = "cheaper" // optimize_for=price
	ProfileFaster  Profile = "faster"  // optimize_for=time
)

func (p Profile) Optimize() tutu.Optimize {
	if p == ProfileFaster {
		return tutu.OptimizeTime
	}
	return tutu.OptimizePrice
}

// Guest is one person travelling to the event.
type Guest struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	City           string  `json:"city"`
	Profile        Profile `json:"profile"`
	Adults         int     `json:"adults"`
	Children       int     `json:"children"`
	NeedsLodging   bool    `json:"needs_lodging"`
	FindCompanions bool    `json:"find_companions"` // opt-in only
	Notes          string  `json:"notes,omitempty"`

	// PinnedKey is the RouteOption key the guest picked themselves from their
	// card. The planner keeps this option chosen while it survives fresh
	// searches; when it vanishes the row collapses honestly.
	PinnedKey string `json:"pinned_key,omitempty"`
	// Purchased means the guest confirmed buying the ticket: the row freezes —
	// no re-planning, no wave swaps, the board shows «куплен».
	Purchased bool `json:"purchased,omitempty"`
	// CompanionConsent: the guest agreed to reveal their name to companions.
	// Names unlock only when BOTH sides of a match consented (ТЗ §5).
	CompanionConsent bool `json:"companion_consent,omitempty"`
}

func (g Guest) Party() int {
	n := g.Adults + g.Children
	if n < 1 {
		return 1
	}
	return n
}

// Event is the gathering being assembled.
type Event struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	InputMode   InputMode `json:"input_mode"`
	Destination string    `json:"destination"`       // place mode
	Vibe        string    `json:"vibe,omitempty"`    // vibe mode raw text
	Date        string    `json:"date"`              // YYYY-MM-DD, the event day
	Deadline    string    `json:"deadline"`          // HH:MM local, gather time
	BufferHours float64   `json:"buffer_hours"`      // be in the city N hours early
	SpacingMin  int       `json:"spacing_min"`       // hug-wave min gap between arrivals
	BudgetPerP  int       `json:"budget_per_person"` // 0 = no budget
	Totalizator bool      `json:"totalizator"`

	Guests []Guest `json:"guests"`

	// VibeCandidates holds ranked candidate cities for InputVibe events (filled
	// by the LLM + live pricing pass). Empty for place mode.
	VibeCandidates []VibeCity `json:"vibe_candidates,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// VibeCity is a candidate destination scored by the total cost of gathering
// everyone there.
type VibeCity struct {
	City       string  `json:"city"`
	TotalPrice float64 `json:"total_price"`
	MaxArrival string  `json:"max_arrival"` // latest guest arrival (human)
	Reachable  int     `json:"reachable"`   // how many guests can make the deadline
	Note       string  `json:"note,omitempty"`
	// Breakdown is the per-guest cost of gathering here — the «увидеться
	// вдвоём» mode shows each side what THEIR half of the smile costs.
	Breakdown []GuestLeg `json:"breakdown,omitempty"`
}

// GuestLeg is one guest's cheapest way into a candidate city.
type GuestLeg struct {
	Guest   string  `json:"guest"`
	From    string  `json:"from"`
	Price   float64 `json:"price"`
	Arrival string  `json:"arrival"` // HH:MM, empty when unreachable
	Mode    string  `json:"mode"`
}

// DeadlineTime computes the hard "must be in the city by" instant:
// event date @ deadline minus the buffer. Times are in +03:00 (MSK), matching
// the MCP payloads, so the whole product reasons in one zone.
func (e Event) DeadlineTime() (time.Time, bool) {
	loc := mskLocation()
	d, err := time.ParseInLocation("2006-01-02 15:04", e.Date+" "+e.Deadline, loc)
	if err != nil {
		return time.Time{}, false
	}
	buf := time.Duration(e.BufferHours * float64(time.Hour))
	return d.Add(-buf), true
}

// GatherTime is the raw gather instant (deadline without buffer subtracted).
func (e Event) GatherTime() (time.Time, bool) {
	loc := mskLocation()
	d, err := time.ParseInLocation("2006-01-02 15:04", e.Date+" "+e.Deadline, loc)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

func mskLocation() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*60*60)
}

// Status is a board row's live state.
type Status string

const (
	StatusAssembled   Status = "assembled"   // собран
	StatusWaiting     Status = "waiting"     // ждёт решения
	StatusRisk        Status = "risk"        // риск
	StatusReassembled Status = "reassembled" // рассыпался → пересобран
	StatusNeedsHelp   Status = "needs_help"  // краснеет, просит помощи
	StatusPlanning    Status = "planning"    // идёт первичный расчёт
	StatusPurchased   Status = "purchased"   // билет куплен — строка заморожена
)

func (s Status) Human() string {
	switch s {
	case StatusAssembled:
		return "собран"
	case StatusWaiting:
		return "ждёт решения"
	case StatusRisk:
		return "риск"
	case StatusReassembled:
		return "пересобран"
	case StatusNeedsHelp:
		return "нужна помощь"
	case StatusPlanning:
		return "считаю…"
	case StatusPurchased:
		return "куплен"
	default:
		return string(s)
	}
}

// RouteOption is a planned, human-facing travel choice for a guest.
type RouteOption struct {
	Mode        tutu.Mode       `json:"mode"`
	ModeHuman   string          `json:"mode_human"`
	FromStation string          `json:"from_station"`
	ToStation   string          `json:"to_station"`
	DepartureAt time.Time       `json:"departure_at"`
	ArrivalAt   time.Time       `json:"arrival_at"`
	DurationMin int             `json:"duration_min"`
	Price       float64         `json:"price"`
	Currency    string          `json:"currency"`
	Transfers   int             `json:"transfers"`
	Number      string          `json:"number"` // train/flight number
	Carrier     string          `json:"carrier"`
	Complex     bool            `json:"complex"`             // interchange / self-transfer
	Via         string          `json:"via,omitempty"`       // "Москва" for transfer plans
	LegLinks    []string        `json:"leg_links,omitempty"` // per-leg checkout URLs for a transfer
	NightBefore bool            `json:"night_before"`
	CheckoutURL string          `json:"checkout_url,omitempty"`
	CheckoutRef json.RawMessage `json:"-"`          // kept server-side for the deeplink build
	DetailsRef  json.RawMessage `json:"-"`          // kept server-side for the seatmap («места рядом»)
	MarginMin   int             `json:"margin_min"` // minutes of slack before the deadline
	// Key is a stable identity for this option (mode|number|departure). It is
	// what a guest pins when they pick an option themselves, and it survives
	// board rebuilds because fresh searches return the same physical vehicle.
	Key string `json:"key"`
}

// OptionKey derives the stable option identity used for guest pinning.
func OptionKey(mode tutu.Mode, number string, departure time.Time) string {
	return string(mode) + "|" + number + "|" + departure.Format(time.RFC3339)
}

// DecisionEntry is one line of the transparency log ("why risk", "why reassembled").
type DecisionEntry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // planned | risk | collapsed | reassembled | help | wave
	Detail string    `json:"detail"`
}

// BoardRow is the live state of one guest on the board.
type BoardRow struct {
	GuestID      string          `json:"guest_id"`
	GuestName    string          `json:"guest_name"`
	City         string          `json:"city"`
	Profile      Profile         `json:"profile"`
	Status       Status          `json:"status"`
	Chosen       *RouteOption    `json:"chosen"`
	Alternatives []RouteOption   `json:"alternatives"`
	HumanCard    string          `json:"human_card"`
	RiskReasons  []string        `json:"risk_reasons"`
	Decisions    []DecisionEntry `json:"decisions"`
	Coord        *tutu.Coord     `json:"coord"`
	LastChecked  time.Time       `json:"last_checked"`
	// WaveShiftMin records how far the hug-wave nudged this arrival, if at all.
	WaveShiftMin int  `json:"wave_shift_min"`
	NeedsLodging bool `json:"needs_lodging"`
	// Purchased mirrors the guest's confirmation; a purchased row is frozen.
	Purchased bool `json:"purchased"`
	// Pinned: the chosen option is the guest's own pick — the wave and the
	// ranker keep their hands off it.
	Pinned bool `json:"pinned"`
	// Hotels are lodging offers near the event for guests flagged «ночлег»
	// (ТЗ §8): whole-stay prices, checkout chain like transport.
	Hotels []tutu.HotelOffer `json:"hotels,omitempty"`
}

// Companion is a discovered shared segment between two opt-in guests.
type Companion struct {
	GuestA  string    `json:"guest_a"`
	GuestB  string    `json:"guest_b"`
	Mode    tutu.Mode `json:"mode"`
	Number  string    `json:"number"`
	Segment string    `json:"segment"` // "Москва — Казань"
	Note    string    `json:"note"`
	// SeatHint quotes a live seats-together proposal from the rail seatmap:
	// «вагон 8 (купе 2Ш), места 34 и 36 рядом — 11332₽ за двоих».
	SeatHint string `json:"seat_hint,omitempty"`
	// MutualConsent: both sides agreed to reveal names to each other.
	MutualConsent bool `json:"mutual_consent"`
}

// Board is the full computed state broadcast to the frontend.
type Board struct {
	EventID     string      `json:"event_id"`
	Rows        []BoardRow  `json:"rows"`
	Companions  []Companion `json:"companions"`
	DestCoord   *tutu.Coord `json:"dest_coord"`
	Destination string      `json:"destination"`
	Deadline    time.Time   `json:"deadline"`
	GatherAt    time.Time   `json:"gather_at"`
	Assembled   int         `json:"assembled"`
	Total       int         `json:"total"`
	UpdatedAt   time.Time   `json:"updated_at"`
	// Totalizator odds, when the module is on.
	Bets []Bet `json:"bets,omitempty"`
}

// Bet is a rofl "will this guest be late?" line with an honest probability.
type Bet struct {
	GuestID    string  `json:"guest_id"`
	GuestName  string  `json:"guest_name"`
	LateChance float64 `json:"late_chance"` // 0..1, computed from route fragility
	Rationale  string  `json:"rationale"`
}
