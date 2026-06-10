package adapter

import (
	"fmt"
	"sync"
)

// ErrNotFound is returned by Registry.Get when no adapter is registered under
// the requested name.
type ErrNotFound struct {
	Name string
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("adapter %q not registered", e.Name)
}

// Registry is a thread-safe map of adapter name → Adapter. It holds no global
// state; callers construct their own Registry and populate it at startup.
//
// Register uses last-write-wins semantics: registering the same name twice
// replaces the earlier entry.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Adapter
}

// NewRegistry returns an empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]Adapter)}
}

// Register adds (or replaces) a in the registry under a.Name().
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[a.Name()] = a
}

// Get returns the adapter registered under name, or *ErrNotFound if none is
// registered.
func (r *Registry) Get(name string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.entries[name]
	if !ok {
		return nil, &ErrNotFound{Name: name}
	}
	return a, nil
}
