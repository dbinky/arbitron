package http_test

import (
	"sync"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_Register_Lookup(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()
	instanceID := uuid.New()
	secret := "hash123"

	reg.Register("token-abc", instanceID, "node-1", &secret)

	entry, ok := reg.Lookup("token-abc")
	require.True(t, ok)
	assert.Equal(t, instanceID, entry.InstanceID)
	assert.Equal(t, "node-1", entry.NodeID)
	require.NotNil(t, entry.SecretHash)
	assert.Equal(t, "hash123", *entry.SecretHash)
}

func TestRegistry_Lookup_NotFound(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()

	_, ok := reg.Lookup("nonexistent")
	assert.False(t, ok)
}

func TestRegistry_Deactivate(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()
	instanceID := uuid.New()

	reg.Register("token-xyz", instanceID, "node-2", nil)
	reg.Deactivate("token-xyz")

	_, ok := reg.Lookup("token-xyz")
	assert.False(t, ok)
}

func TestRegistry_Deactivate_NonExistent_NoOp(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()

	// Should not panic.
	reg.Deactivate("nonexistent")
}

func TestRegistry_ActiveCount_Empty(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()
	assert.Equal(t, 0, reg.ActiveCount())
}

func TestRegistry_ActiveCount_AfterRegistrations(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()

	reg.Register("t1", uuid.New(), "n1", nil)
	reg.Register("t2", uuid.New(), "n2", nil)
	reg.Register("t3", uuid.New(), "n3", nil)

	assert.Equal(t, 3, reg.ActiveCount())
}

func TestRegistry_ActiveCount_AfterDeactivation(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()

	reg.Register("t1", uuid.New(), "n1", nil)
	reg.Register("t2", uuid.New(), "n2", nil)
	reg.Deactivate("t1")

	assert.Equal(t, 1, reg.ActiveCount())
}

func TestRegistry_Register_OverwritesSameToken(t *testing.T) {
	// Re-registering the same token (crash recovery path) must overwrite
	// the previous entry, not accumulate duplicates.
	reg := adapthttp.NewWebhookRegistry()
	id1 := uuid.New()
	id2 := uuid.New()

	reg.Register("token-reuse", id1, "node-old", nil)
	reg.Register("token-reuse", id2, "node-new", nil)

	entry, ok := reg.Lookup("token-reuse")
	require.True(t, ok)
	assert.Equal(t, id2, entry.InstanceID, "re-registration must overwrite the previous entry")
	assert.Equal(t, "node-new", entry.NodeID)
	assert.Equal(t, 1, reg.ActiveCount(), "overwrite must not increase count")
}

func TestRegistry_Lookup_NoSecretHash(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()
	instanceID := uuid.New()

	reg.Register("no-secret-token", instanceID, "node-x", nil)

	entry, ok := reg.Lookup("no-secret-token")
	require.True(t, ok)
	assert.Equal(t, instanceID, entry.InstanceID)
	assert.Nil(t, entry.SecretHash)
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := adapthttp.NewWebhookRegistry()
	const goroutines = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		token := uuid.New().String()
		instanceID := uuid.New()

		go func() {
			defer wg.Done()
			reg.Register(token, instanceID, "node", nil)
		}()

		go func() {
			defer wg.Done()
			reg.Lookup(token)
		}()

		go func() {
			defer wg.Done()
			reg.Deactivate(token)
		}()
	}

	wg.Wait()
}
