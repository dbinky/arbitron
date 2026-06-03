package config_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
)

// Success: a partial SetTeams updates only the named field and returns the
// resulting settings; the backing config is updated in place.
func TestSettingsStore_SetTeams_Partial(t *testing.T) {
	backing := config.TeamsConfig{WebhookURL: "https://old.example.com/hook", Enabled: false}
	store := config.NewSettingsStore(&backing)

	enabled := true
	got := store.SetTeams(nil, &enabled)
	assert.Equal(t, "https://old.example.com/hook", got.WebhookURL, "nil webhookURL leaves it unchanged")
	assert.True(t, got.Enabled)

	newURL := "https://new.example.com/hook"
	got = store.SetTeams(&newURL, nil)
	assert.Equal(t, "https://new.example.com/hook", got.WebhookURL)
	assert.True(t, got.Enabled, "nil enabled leaves it unchanged")

	// The store writes through to the backing TeamsConfig (single source of truth).
	assert.Equal(t, "https://new.example.com/hook", backing.WebhookURL)
	assert.True(t, backing.Enabled)
}

// Edge-case: TeamsWebhook and GetTeams reflect the current state.
func TestSettingsStore_Accessors(t *testing.T) {
	backing := config.TeamsConfig{WebhookURL: "https://x.example.com/h", Enabled: true}
	store := config.NewSettingsStore(&backing)

	url, enabled := store.TeamsWebhook()
	assert.Equal(t, "https://x.example.com/h", url)
	assert.True(t, enabled)
	assert.Equal(t, backing, store.GetTeams())
}

// Concurrency: the store serializes the notifier's live reads against admin
// writes from the HTTP and MCP transports. MUST be run under `go test -race`
// (the Makefile gate does); before the store existed these three parties raced
// on config.Notifications.Teams with no shared lock.
func TestSettingsStore_ConcurrentReadWrite_RaceFree(t *testing.T) {
	backing := config.TeamsConfig{}
	store := config.NewSettingsStore(&backing)

	const goroutines = 8
	const iterations = 500

	var wg sync.WaitGroup

	// Writers: simulate HTTP + MCP admin updates.
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				url := "https://example.com/hook"
				enabled := i%2 == 0
				store.SetTeams(&url, &enabled)
			}
		}()
	}

	// Readers: simulate the Teams notifier's live read on engine goroutines.
	for r := 0; r < goroutines; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, _ = store.TeamsWebhook()
				_ = store.GetTeams()
			}
		}()
	}

	wg.Wait()

	// After the storm the URL is the only value writers ever set.
	require.Equal(t, "https://example.com/hook", store.GetTeams().WebhookURL)
}
