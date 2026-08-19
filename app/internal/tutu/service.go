package tutu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/mcp"
)

// Service is the domain-level API over the MCP client.
type Service struct {
	mcp *mcp.Client
}

func NewService(c *mcp.Client) *Service { return &Service{mcp: c} }

// BreakerOpen mirrors the MCP client's circuit state so schedulers can pause
// instead of burning cycles against a cooling server.
func (s *Service) BreakerOpen() (bool, time.Time) { return s.mcp.BreakerOpen() }

// Optimize selects the multitransport ranking axis.
type Optimize string

const (
	OptimizePrice Optimize = "price"
	OptimizeTime  Optimize = "time"
)

// SearchMulti runs search_multitransport for a single adult party. When fresh
// is true the TTL cache is bypassed — the recheck scheduler needs live prices
// and live inventory to detect a route that has collapsed.
func (s *Service) SearchMulti(ctx context.Context, origin, dest, date string, adults int, opt Optimize, modes []Mode, fresh bool) (*MultiResult, error) {
	args := map[string]any{
		"origin":         origin,
		"destination":    dest,
		"departure_date": date,
		"adults":         adults,
		"optimize_for":   string(opt),
		"page_size":      30,
	}
	if len(modes) > 0 {
		ms := make([]string, len(modes))
		for i, m := range modes {
			ms[i] = string(m)
		}
		args["modes"] = ms
	}
	raw, err := s.mcp.CallTool(ctx, "search_multitransport", args, fresh)
	if err != nil {
		return nil, err
	}
	return parseMulti(raw)
}

// SearchRail runs a single-mode rail search — used for the "night train on the
// previous date" pass and for parties with children (multitransport is
// adults-only).
func (s *Service) SearchRail(ctx context.Context, origin, dest, date string, passengers int, fresh bool) (*RailResult, error) {
	args := map[string]any{
		"origin":         origin,
		"destination":    dest,
		"departure_date": date,
		"passengers":     passengers,
		"page_size":      30,
	}
	raw, err := s.mcp.CallTool(ctx, "search_rail", args, fresh)
	if err != nil {
		return nil, err
	}
	var rr RailResult
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("parse rail: %w", err)
	}
	if len(rr.Meta) > 0 {
		var meta struct {
			Interchange []InterchangePlan `json:"interchange_routes"`
		}
		if err := json.Unmarshal(rr.Meta, &meta); err == nil {
			rr.Interchange = meta.Interchange
		}
	}
	return &rr, nil
}

// SearchAvia runs single-mode avia (supports children, unlike multitransport).
func (s *Service) SearchAvia(ctx context.Context, origin, dest, date string, adults, children int) ([]Variant, error) {
	args := map[string]any{
		"origin":         origin,
		"destination":    dest,
		"departure_date": date,
		"adults":         adults,
		"children":       children,
		"page_size":      30,
	}
	raw, err := s.mcp.CallTool(ctx, "search_avia", args, false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Offers []Variant `json:"offers"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse avia: %w", err)
	}
	return res.Offers, nil
}

// CreateCheckoutLink hands the offer's checkout_ref back to the server verbatim
// and returns the opaque purchase URL. The ref is passed as-is: never rebuilt.
func (s *Service) CreateCheckoutLink(ctx context.Context, ref json.RawMessage) (*CheckoutLink, error) {
	if len(ref) == 0 {
		return nil, fmt.Errorf("empty checkout_ref")
	}
	var args map[string]any
	if err := json.Unmarshal(ref, &args); err != nil {
		return nil, fmt.Errorf("checkout_ref not an object: %w", err)
	}
	raw, err := s.mcp.CallTool(ctx, "create_checkout_link", args, true) // never cache checkout
	if err != nil {
		return nil, err
	}
	var link CheckoutLink
	if err := json.Unmarshal(raw, &link); err != nil {
		return nil, fmt.Errorf("parse checkout link: %w", err)
	}
	return &link, nil
}

// parseMulti unpacks variants and the parts of meta the orchestrator reads.
func parseMulti(raw json.RawMessage) (*MultiResult, error) {
	var mr MultiResult
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("parse multitransport: %w", err)
	}
	if len(mr.Meta) > 0 {
		var meta struct {
			ModesSummary map[string]ModeStat `json:"modes_summary"`
			Unavailable  []string            `json:"unavailable"`
		}
		if err := json.Unmarshal(mr.Meta, &meta); err == nil {
			mr.ModesSummary = meta.ModesSummary
			mr.Unavailable = meta.Unavailable
		}
	}
	return &mr, nil
}
