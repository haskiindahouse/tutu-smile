package event

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// State is the live server-side state of one event: its definition, the last
// computed board, and the set of SSE subscribers watching it.
type State struct {
	Event Event
	Board *Board

	mu   sync.RWMutex
	subs map[int]chan *Board
	next int
}

// Store keeps all events in memory (no accounts, no DB — a card is a link).
type Store struct {
	mu     sync.RWMutex
	states map[string]*State
}

func NewStore() *Store {
	return &Store{states: map[string]*State{}}
}

// Create registers a new event and returns its assigned id. A caller-supplied
// id that is already taken is replaced with a fresh one: Create never silently
// overwrites a live event.
func (s *Store) Create(ev Event) *State {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	for i := range ev.Guests {
		if ev.Guests[i].ID == "" {
			ev.Guests[i].ID = newID()
		}
		if ev.Guests[i].Adults < 1 {
			ev.Guests[i].Adults = 1
		}
	}
	s.mu.Lock()
	if ev.ID == "" || s.states[ev.ID] != nil {
		ev.ID = newID()
	}
	st := &State{Event: ev, subs: map[int]chan *Board{}}
	s.states[ev.ID] = st
	s.mu.Unlock()
	return st
}

func (s *Store) Get(id string) (*State, bool) {
	s.mu.RLock()
	st, ok := s.states[id]
	s.mu.RUnlock()
	return st, ok
}

func (s *Store) List() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, st.Snapshot())
	}
	return out
}

// SetBoard stores the latest board and broadcasts it to subscribers. The
// broadcast runs under the lock: the sends are non-blocking, and holding the
// lock is what keeps a concurrent unsubscribe from closing a channel mid-send
// (send on a closed channel panics).
func (st *State) SetBoard(b *Board) {
	st.mu.Lock()
	st.Board = b
	for _, ch := range st.subs {
		select {
		case ch <- b:
		default: // drop if the subscriber is slow; they'll get the next one
		}
	}
	st.mu.Unlock()
}

func (st *State) CurrentBoard() *Board {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.Board
}

func (st *State) Snapshot() Event {
	st.mu.RLock()
	defer st.mu.RUnlock()
	ev := st.Event
	// UpdateEvent mutates Guests in place — hand callers their own copy of the
	// slice so reading a snapshot never races a concurrent guest mutation.
	if st.Event.Guests != nil {
		ev.Guests = append([]Guest{}, st.Event.Guests...)
	}
	return ev
}

func (st *State) UpdateEvent(mut func(*Event)) {
	st.mu.Lock()
	mut(&st.Event)
	st.mu.Unlock()
}

// Subscribe returns a channel of board updates and an unsubscribe func.
func (st *State) Subscribe() (<-chan *Board, func()) {
	st.mu.Lock()
	id := st.next
	st.next++
	ch := make(chan *Board, 4)
	st.subs[id] = ch
	// Prime with the current board so a new watcher renders immediately.
	if st.Board != nil {
		select {
		case ch <- st.Board:
		default:
		}
	}
	st.mu.Unlock()
	return ch, func() {
		st.mu.Lock()
		if c, ok := st.subs[id]; ok {
			delete(st.subs, id)
			close(c)
		}
		st.mu.Unlock()
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
