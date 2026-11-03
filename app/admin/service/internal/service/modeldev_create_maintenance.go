package service

import (
	"context"
	"sync"
)

// ModelDevCreateMaintenance coordinates Create admission and its existing
// publisher during the bounded deployment maintenance window. Close/release
// work never enters this gate. Configuration is fixed before server startup.
type ModelDevCreateMaintenance struct {
	mu      sync.Mutex
	paused  bool
	active  int
	drained chan struct{}
}

func NewModelDevCreateMaintenance(paused bool) *ModelDevCreateMaintenance {
	drained := make(chan struct{})
	close(drained)
	return &ModelDevCreateMaintenance{paused: paused, drained: drained}
}
func (g *ModelDevCreateMaintenance) begin() (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	if g.paused {
		g.mu.Unlock()
		return nil, false
	}
	if g.active == 0 {
		g.drained = make(chan struct{})
	}
	g.active++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.active == 0 {
				close(g.drained)
			}
		})
	}, true
}
func (g *ModelDevCreateMaintenance) pauseAndDrain(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	g.paused = true
	drained := g.drained
	g.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetCreateMaintenance is composition-only wiring, before serving requests.
func (s *ModelDevService) SetCreateMaintenance(g *ModelDevCreateMaintenance) {
	if s != nil {
		s.createMaintenance = g
	}
}
func (w *ModelDevDispatchWorker) SetCreateMaintenance(g *ModelDevCreateMaintenance) {
	if w != nil {
		w.createMaintenance = g
	}
}
