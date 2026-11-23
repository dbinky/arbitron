package main

import (
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

type runModelConsumers struct {
	llm         *engine.LLMCallExecutor
	agent       *engine.AgentCallExecutor
	childInputs *engine.LLMChildInputPreparer
}

// newRunModelConsumers constructs the batch consumers used by the composition
// root. Each resolves models inside an executing run, including preparation
// calls made on behalf of the parent before spawning a child. The root passes
// the runner pool's routed selectors, so every call is placed on a runner.
func newRunModelConsumers(fr engine.FileReader, chats engine.ChatProviderSelector, agents engine.AgentProviderSelector, pins *provider.PinningResolver, defaultBackend string) runModelConsumers {
	// Losing the run ID must never turn a batch invocation into live model
	// resolution. Valid legacy run IDs still deliberately resolve live.
	resolver := pins.ForRun()
	return runModelConsumers{
		llm:         engine.NewLLMCallExecutor(fr, chats, resolver),
		agent:       engine.NewAgentCallExecutor(fr, agents, resolver, defaultBackend),
		childInputs: engine.NewChildInputPreparer(chats, resolver),
	}
}
