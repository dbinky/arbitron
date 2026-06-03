package config

import "sync"

// SettingsStore is the single synchronized owner of the runtime-mutable Teams
// settings. Before it existed, three parties touched config.Notifications.Teams
// with three different disciplines — the MCP tool wrote with no lock, the HTTP
// handler wrote under its own mutex, and the Teams notifier read live on engine
// goroutines — so nothing actually guarded the fields. Routing every read and
// write through this store collapses those into one mutex.
//
// The store guards the Teams region of an existing *Config in place, so the
// loaded configuration remains the seed and single backing storage; callers
// must not touch config.Notifications.Teams directly once a store wraps it.
type SettingsStore struct {
	mu    sync.RWMutex
	teams *TeamsConfig
}

// NewSettingsStore returns a store guarding the given Teams settings. Pass
// &cfg.Notifications.Teams so the loaded config seeds and backs the store.
func NewSettingsStore(teams *TeamsConfig) *SettingsStore {
	return &SettingsStore{teams: teams}
}

// GetTeams returns a copy of the current Teams settings under the read lock.
func (s *SettingsStore) GetTeams() TeamsConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.teams
}

// SetTeams applies a partial update under the write lock: a nil pointer leaves
// that field unchanged. It returns the resulting settings so callers can build
// a response without taking the lock a second time.
func (s *SettingsStore) SetTeams(webhookURL *string, enabled *bool) TeamsConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if webhookURL != nil {
		s.teams.WebhookURL = *webhookURL
	}
	if enabled != nil {
		s.teams.Enabled = *enabled
	}
	return *s.teams
}

// TeamsWebhook satisfies the notifier's narrow read port: it returns the live
// webhook URL and enable flag under the read lock, so the former live-read race
// against concurrent admin updates disappears.
func (s *SettingsStore) TeamsWebhook() (url string, enabled bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.teams.WebhookURL, s.teams.Enabled
}
