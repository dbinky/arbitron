package runner

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

func TestSessionImport_OutputShapeRequiresOnePayload(t *testing.T) {
	out := StepOutput{Session: &SessionOpResult{SessionID: "native-import"}}
	assert.True(t, validOutput(OpSessionImport, out))
	assert.False(t, validOutput(OpSessionImport, StepOutput{}))
	assert.False(t, validOutput(OpChat, out))
	out.Chat = &engine.ChatResponse{Output: "unrelated"}
	assert.False(t, validOutput(OpSessionImport, out))
	assert.False(t, validOutput(OpChat, out))
}
