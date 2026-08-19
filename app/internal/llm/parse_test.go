package llm

import "testing"

func TestExtractJSONUnwrapsFences(t *testing.T) {
	in := "```json\n{\"cities\":[\"Казань\"]}\n```"
	if got := extractJSON(in); got != `{"cities":["Казань"]}` {
		t.Fatalf("extractJSON = %q", got)
	}
}

func TestNormalizeDraftFillsGapsHonestly(t *testing.T) {
	d := EventDraft{Guests: []DraftGuest{{Name: "Паша", City: "Пермь", Profile: "flying"}}}
	normalizeDraft(&d)
	if d.Deadline != "15:00" {
		t.Fatalf("default deadline expected, got %s", d.Deadline)
	}
	if d.Guests[0].Profile != "cheaper" {
		t.Fatalf("unknown profile must fall back to cheaper, got %s", d.Guests[0].Profile)
	}
	if d.Guests[0].Adults != 1 {
		t.Fatalf("adults floor is 1, got %d", d.Guests[0].Adults)
	}
	// Missing must call out the absent destination and date, once each.
	normalizeDraft(&d)
	countDest := 0
	for _, m := range d.Missing {
		if m == "город события или пожелание" {
			countDest++
		}
	}
	if countDest != 1 {
		t.Fatalf("missing entries must not duplicate, got %v", d.Missing)
	}
}
