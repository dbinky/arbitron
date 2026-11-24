package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

type turnBindingRestorerSetter interface {
	SetTurnBindingRestorer(engine.TurnBindingRestorer)
}

func TestSetTurnBindingRestorer_RejectsNil(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, _ := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	assert.Panics(t, func() { h.(turnBindingRestorerSetter).SetTurnBindingRestorer(nil) })

	iRepo, neRepo, illm, files, ibus, iqueue, _ := interactiveTestSetup()
	exec := buildInteractiveExecutor(iRepo, neRepo, illm, files, ibus, iqueue)
	assert.Panics(t, func() { exec.SetTurnBindingRestorer(nil) })

	f := newBridgeFixture(t)
	assert.Panics(t, func() { f.bridge.SetTurnBindingRestorer(nil) })
}
