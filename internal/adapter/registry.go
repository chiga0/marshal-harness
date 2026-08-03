// Package adapter contains Worker adapter boundary infrastructure. Concrete
// provider adapters are introduced only after the deterministic Core exists.
package adapter

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/port"
)

// Registry resolves exact adapter IDs and never performs implicit fallback.
type Registry struct {
	mu      sync.RWMutex
	workers map[string]port.WorkerAdapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{workers: make(map[string]port.WorkerAdapter)}
}

// Register rejects empty and duplicate IDs.
func (r *Registry) Register(worker port.WorkerAdapter) error {
	if worker == nil {
		return fmt.Errorf("register worker: nil adapter")
	}
	id := strings.TrimSpace(worker.ID())
	if id == "" {
		return fmt.Errorf("register worker: empty adapter ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.workers[id]; exists {
		return fmt.Errorf("register worker %q: duplicate adapter ID", id)
	}
	r.workers[id] = worker
	return nil
}

// Resolve returns only an exact adapter ID match.
func (r *Registry) Resolve(id string) (port.WorkerAdapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	worker, exists := r.workers[id]
	if !exists {
		return nil, fmt.Errorf("resolve worker %q: adapter not registered", id)
	}
	return worker, nil
}

// IDs returns registered IDs in deterministic order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.workers))
	for id := range r.workers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
