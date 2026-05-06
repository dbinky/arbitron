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
