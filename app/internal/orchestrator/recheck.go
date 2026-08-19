package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/ma4ypic4y/tutu-smile/internal/event"
)

// Manager owns the background re-verification of live boards. Because the Tutu
// inventory is live and offers die, each event's board is periodically rebuilt
// from fresh searches; a row whose route no longer holds is reassembled, and if
// that fails the row goes red and the board asks for help.
type Manager struct {
	orch  *Orchestrator
	store *event.Store
	every time.Duration

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewManager(orch *Orchestrator, store *event.Store, every time.Duration) *Manager {
	return &Manager{
		orch:    orch,
		store:   store,
		every:   every,
		running: map[string]context.CancelFunc{},
	}
}

// BuildAndStore computes a fresh board synchronously and stores/broadcasts it.
func (m *Manager) BuildAndStore(ctx context.Context, st *event.State, fresh bool) *event.Board {
	ev := st.Snapshot()
	prior := st.CurrentBoard()
	board := m.orch.BuildBoard(ctx, ev, prior, fresh)
	st.SetBoard(board)

	// A guest's pinned option that vanished from live inventory was already
	// logged as a collapse; drop the stale pin so later rebuilds rank freely
	// instead of re-mourning it every cycle.
	var stale []string
	for _, r := range board.Rows {
		for _, g := range ev.Guests {
			if g.ID == r.GuestID && g.PinnedKey != "" && !g.Purchased && !r.Pinned {
				stale = append(stale, g.ID)
			}
		}
	}
	if len(stale) > 0 {
		st.UpdateEvent(func(e *event.Event) {
			for _, id := range stale {
				for i := range e.Guests {
					if e.Guests[i].ID == id {
						e.Guests[i].PinnedKey = ""
					}
				}
			}
		})
	}
	return board
}

// Start launches the periodic recheck loop for an event (idempotent).
func (m *Manager) Start(id string) {
	m.mu.Lock()
	if _, ok := m.running[id]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running[id] = cancel
	m.mu.Unlock()

	go m.loop(ctx, id)
}

func (m *Manager) Stop(id string) {
	m.mu.Lock()
	if cancel, ok := m.running[id]; ok {
		cancel()
		delete(m.running, id)
	}
	m.mu.Unlock()
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	for _, cancel := range m.running {
		cancel()
	}
	m.running = map[string]context.CancelFunc{}
	m.mu.Unlock()
}

func allPurchased(ev event.Event) bool {
	if len(ev.Guests) == 0 {
		return false
	}
	for _, g := range ev.Guests {
		if !g.Purchased {
			return false
		}
	}
	return true
}

func (m *Manager) loop(ctx context.Context, id string) {
	t := time.NewTicker(m.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, ok := m.store.Get(id)
			if !ok {
				return
			}
			// The MCP is cooling down after a failure streak — a recheck now
			// would only feed the fire. The board keeps its last honest state.
			if open, _ := m.orch.svc.BreakerOpen(); open {
				continue
			}
			ev := st.Snapshot()
			// Past the gather time there is nothing left to re-verify.
			if g, ok := ev.GatherTime(); ok && time.Now().After(g) {
				return
			}
			// Everyone bought their ticket: nothing live remains on the board.
			if allPurchased(ev) {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, m.every)
			m.BuildAndStore(rctx, st, true)
			cancel()
		}
	}
}
