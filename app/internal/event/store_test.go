package event

import (
	"sync"
	"testing"
)

// SetBoard racing an unsubscribe must never panic with "send on closed
// channel" — run with -race to also catch the close/send data race.
func TestSetBoardUnsubscribeRace(t *testing.T) {
	st := &State{subs: map[int]chan *Board{}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		ch, unsub := st.Subscribe()
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range ch {
			}
		}()
		go func() {
			defer wg.Done()
			unsub()
		}()
	}
	b := &Board{}
	for i := 0; i < 1000; i++ {
		st.SetBoard(b)
	}
	wg.Wait()
}

// UpdateEvent mutates guests in place, so a Snapshot must not share the
// guests slice with the live event.
func TestSnapshotGuestsAreCopied(t *testing.T) {
	st := &State{
		Event: Event{Guests: []Guest{{ID: "g1", Name: "Аня"}}},
		subs:  map[int]chan *Board{},
	}
	snap := st.Snapshot()
	st.UpdateEvent(func(ev *Event) { ev.Guests[0].Name = "Борис" })
	if snap.Guests[0].Name != "Аня" {
		t.Fatalf("snapshot shares guest storage with the live event: %q", snap.Guests[0].Name)
	}
}

// A caller-supplied id that is already taken must be replaced, not overwrite
// the live event.
func TestCreateNeverOverwritesExistingID(t *testing.T) {
	s := NewStore()
	first := s.Create(Event{ID: "fixed", Name: "первый"})
	second := s.Create(Event{ID: "fixed", Name: "второй"})
	if second.Event.ID == "fixed" {
		t.Fatalf("second Create reused a taken id")
	}
	if got, ok := s.Get("fixed"); !ok || got != first {
		t.Fatalf("original event was overwritten")
	}
}
