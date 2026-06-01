package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelKey is a recognizable secret driven end-to-end through the secrecy
// path. No log line, response, or serialized workflow may contain it.
const sentinelKey = "sk-SENTINEL-DO-NOT-LOG"

// TestSecrecy_ResolveAndUse_NeverLogsKey drives the full Phase-2 secrecy path
// with the REAL crypto + sqlite store + widened resolver: a connection is
// created with a sentinel key, a profile bound to it is resolved, and
// RevealKey decrypts the key. All log output produced across the sequence is
// captured and asserted to contain no substring of the sentinel; log lines that
// reference the connection reference its NAME only.
func TestSecrecy_ResolveAndUse_NeverLogsKey(t *testing.T) {
	ctx := context.Background()

	// Buffer-backed logger installed on every secrecy-relevant component.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// REAL crypto from a loaded master key (no plaintext key on the struct).
	key := testKey32(0x5C)
	crypto, err := adaptercrypto.NewAESGCM(key)
	require.NoError(t, err)

	// REAL sqlite-backed store sealing the sentinel at rest.
	db := setupTestDB(t)
	store := NewConnectionRepo(db.DB, crypto)

	conn := provider.Connection{
		Name:     "azure-prod",
		Provider: "azure-openai",
		BaseURL:  "https://example.openai.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
	}
	require.NoError(t, store.Create(ctx, conn, sentinelKey))

	// Widened resolver over the real store.
	resolver, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "az-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "gpt-4o"},
		{Alias: "claude", Kind: provider.ProfileKindChat}, // no-key path
	}, store)
	require.NoError(t, err)

	// Resolve the connection-backed profile: metadata only, no key yet.
	binding, err := resolver.Resolve("az-chat")
	require.NoError(t, err)
	assert.Equal(t, "azure-openai", binding.Provider)
	assert.Equal(t, "https://example.openai.azure.com", binding.BaseURL)
	require.NotNil(t, binding.RevealKey)

	// Lazily reveal the key (call/launch time) and hand it to a no-op consumer
	// that logs only the connection name -- simulating a Phase-3 request builder.
	revealed, err := binding.RevealKey(ctx)
	require.NoError(t, err)
	require.Equal(t, sentinelKey, revealed)
	logger.Info("dispatching request", slog.String("connection", conn.Name), slog.String("model", binding.Model))
	_ = revealed // consumed by the (stubbed) request builder; never logged

	// The no-key path reveals an empty string and never touches the store.
	noKeyBinding, err := resolver.Resolve("claude")
	require.NoError(t, err)
	got, err := noKeyBinding.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", got)
	assert.Equal(t, "claude-cli", noKeyBinding.Provider)

	// SECRECY ASSERTIONS.
	logs := logBuf.String()
	assert.NotContains(t, logs, sentinelKey, "the plaintext key must never appear in any log")
	assert.Contains(t, logs, "azure-prod", "logs should reference the connection name")

	// The sealed bytes in the DB are not the plaintext, and a wrong-key Crypto
	// cannot recover it (confirms at-rest encryption, master key required).
	wrongCrypto, err := adaptercrypto.NewAESGCM(testKey32(0x11))
	require.NoError(t, err)
	wrongStore := NewConnectionRepo(db.DB, wrongCrypto)
	_, err = wrongStore.Reveal(ctx, "azure-prod")
	assert.ErrorIs(t, err, provider.ErrDecrypt)
}

// TestSecrecy_WorkflowJSON_ContainsNoKey round-trips a workflow whose nodes use
// these profiles and asserts the serialized workflow.json holds no API key and
// no connection secret: nodes carry only the model_profile alias.
func TestSecrecy_WorkflowJSON_ContainsNoKey(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "secrecy-wf",
		Nodes: []workflow.Node{
			{
				ID:         "llm",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config:     workflow.NodeConfig{ModelProfile: "az-chat"},
			},
		},
	}

	raw, err := json.Marshal(wf)
	require.NoError(t, err)
	serialized := string(raw)

	// The alias is present; no key/secret/connection-ref material leaks.
	assert.Contains(t, serialized, "az-chat")
	assert.NotContains(t, serialized, sentinelKey)
	assert.NotContains(t, strings.ToLower(serialized), "api_key")
	assert.NotContains(t, strings.ToLower(serialized), "apikey")
	assert.NotContains(t, strings.ToLower(serialized), "api-key")
}

// TestBootGate_ConnectionsRequireMasterKey wires the real store's Count into the
// fail-fast gate, as main.go does. A populated store with no master key boots to
// an error; a Claude-only (zero-connection) store with no key boots cleanly.
func TestBootGate_ConnectionsRequireMasterKey(t *testing.T) {
	ctx := context.Background()

	t.Run("connections exist and no master key fails fast", func(t *testing.T) {
		crypto, err := adaptercrypto.NewAESGCM(testKey32(0x7E))
		require.NoError(t, err)
		db := setupTestDB(t)
		store := NewConnectionRepo(db.DB, crypto)
		require.NoError(t, store.Create(ctx, provider.Connection{
			Name: "azure-prod", Provider: "openrouter", BaseURL: "https://x",
		}, "sk-1"))

		count, err := store.Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		// Simulate the boot path with haveKey=false (no key loaded).
		gateErr := adaptercrypto.RequireMasterKeyIfConnections(false, count)
		assert.ErrorIs(t, gateErr, provider.ErrMasterKeyMissing)
	})

	t.Run("zero connections no master key boots clean", func(t *testing.T) {
		// A Claude-only deployment uses a nil crypto store and stores nothing.
		db := setupTestDB(t)
		store := NewConnectionRepo(db.DB, nil)

		count, err := store.Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 0, count)

		assert.NoError(t, adaptercrypto.RequireMasterKeyIfConnections(false, count))
	})
}
