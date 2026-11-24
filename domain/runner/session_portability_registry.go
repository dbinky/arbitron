package runner

import "github.com/arbitron/arbitron/domain/engine"

// SessionPortabilityRegistry maps this machine's harness backends to their
// session adapters. It never invents a backend.
type SessionPortabilityRegistry struct {
	byBackend map[string]engine.SessionPortability
}

// NewSessionPortabilityRegistry copies entries, dropping nil adapters and empty names.
func NewSessionPortabilityRegistry(entries map[string]engine.SessionPortability) *SessionPortabilityRegistry {
	adapters := make(map[string]engine.SessionPortability, len(entries))
	for backend, adapter := range entries {
		if backend != "" && adapter != nil {
			adapters[backend] = adapter
		}
	}
	return &SessionPortabilityRegistry{byBackend: adapters}
}

// For returns the registered adapter. A nil registry has no adapters.
func (r *SessionPortabilityRegistry) For(backend string) (engine.SessionPortability, bool) {
	if r == nil {
		return nil, false
	}
	adapter, ok := r.byBackend[backend]
	return adapter, ok
}
