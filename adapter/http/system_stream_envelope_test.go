package http

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemStream_EnvelopeIncludesInstanceID(t *testing.T) {
	instID := uuid.New()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// systemWSEnvelope is the shape written to system-WS clients.
	err := enc.Encode(systemWSEnvelope{
		InstanceID: instID,
		Type:       "node.started",
		Timestamp:  time.Unix(0, 0).UTC(),
		Data:       map[string]any{"nodeId": "A"},
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, instID.String(), got["instanceId"])
	assert.Equal(t, "node.started", got["type"])
	nested, ok := got["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "A", nested["nodeId"])
}
