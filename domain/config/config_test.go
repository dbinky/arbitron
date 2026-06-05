package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQueueConfigDefaults(t *testing.T) {
	t.Run("zero value means unlimited", func(t *testing.T) {
		cfg := QueueConfig{}
		assert.Equal(t, 0, cfg.GlobalCap, "zero GlobalCap means unlimited")
		assert.Equal(t, 0, cfg.PerUserCap, "zero PerUserCap means unlimited")
	})

	t.Run("explicit values are preserved", func(t *testing.T) {
		cfg := QueueConfig{GlobalCap: 20, PerUserCap: 5}
		assert.Equal(t, 20, cfg.GlobalCap)
		assert.Equal(t, 5, cfg.PerUserCap)
	})
}

func TestConfigIncludesQueueField(t *testing.T) {
	cfg := Config{
		Queue: QueueConfig{GlobalCap: 10, PerUserCap: 3},
	}
	assert.Equal(t, 10, cfg.Queue.GlobalCap)
	assert.Equal(t, 3, cfg.Queue.PerUserCap)
}

// Happy-path: the new notifications/teams fields exist and round-trip values.
func TestConfig_TeamsFields_RoundTrip(t *testing.T) {
	cfg := Config{
		Notifications: NotificationsConfig{
			Teams: TeamsConfig{
				WebhookURL: "https://example.com/webhook",
				Enabled:    true,
			},
		},
	}

	assert.Equal(t, "https://example.com/webhook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
}

// Edge-case: zero value is a disabled, empty Teams config.
func TestConfig_TeamsFields_ZeroValue(t *testing.T) {
	var cfg Config

	assert.Equal(t, "", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Happy-path: ServerConfig exposes a static public base URL for deep links.
func TestServerConfig_PublicBaseURL_RoundTrip(t *testing.T) {
	sc := ServerConfig{PublicBaseURL: "http://localhost:8080"}

	assert.Equal(t, "http://localhost:8080", sc.PublicBaseURL)
}

// Happy-path: RBACConfig fields round-trip without mutation.
func TestRBACConfig_RoundTrip(t *testing.T) {
	cfg := RBACConfig{
		GroupClaim:   "groups",
		DefaultGroup: "default",
		Leads:        map[string][]string{"engineering": {"alice@corp.example"}},
	}
	assert.Equal(t, "groups", cfg.GroupClaim)
	assert.Equal(t, "default", cfg.DefaultGroup)
	assert.Equal(t, []string{"alice@corp.example"}, cfg.Leads["engineering"])
}

// Edge: zero-value RBACConfig is a valid "no RBAC" config (local-mode equivalent).
func TestRBACConfig_ZeroValue(t *testing.T) {
	var cfg RBACConfig
	assert.Equal(t, "", cfg.GroupClaim, "empty GroupClaim means no claim is read")
	assert.Equal(t, "", cfg.DefaultGroup, "empty DefaultGroup defers to the default sentinel in GroupConfig.DefaultGroupOr")
	assert.Nil(t, cfg.Leads)
}
