package tutu

import (
	"encoding/json"
	"testing"
)

// The MCP documents hotel_id as a string alias, but some payloads carry a
// bare number — both must parse instead of failing the whole search.
func TestHotelRowToleratesStringAndNumericIDs(t *testing.T) {
	cases := []struct {
		payload string
		want    string
	}{
		{`{"hotel_id":"alias-abc"}`, "alias-abc"},
		{`{"hotel_id":12345}`, "12345"},
		{`{"hotel_id":null}`, ""},
		{`{}`, ""},
	}
	for _, c := range cases {
		var h hotelRow
		if err := json.Unmarshal([]byte(c.payload), &h); err != nil {
			t.Fatalf("%s: %v", c.payload, err)
		}
		if got := hotelIDString(h.HotelID); got != c.want {
			t.Fatalf("%s: hotelIDString = %q, want %q", c.payload, got, c.want)
		}
	}
}
