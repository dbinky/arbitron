package main

import "testing"

// Keep a single acceptance gate for the five production resolution consumers.
// Each scenario constructs initDomain over migrated SQLite and asserts the
// outgoing HTTP model or subprocess argv after editing the live profile.
func TestModelPinsWiringAllConsumers(t *testing.T) {
	t.Run("llm_call", TestModelPinsWiringLLMCallSink)
	t.Run("agent_call", TestModelPinsWiringAgentCallSink)
	t.Run("child_input", TestModelPinsWiringChildInputSink)
	// The first turn and resumed turn exercise InteractiveExecutor and the
	// authorized conversation handler, respectively.
	t.Run("interactive_and_continuation", TestModelPinsWiringInteractiveResumeSink)
}

func TestModelPinsDefaultBackendWiring(t *testing.T) {
	// This scenario intentionally leaves Profile.Backend empty on a Codex-default
	// installation, then asserts persisted X/Y and actual fresh/resumed -m argv.
	t.Run("codex", TestModelPinsWiringCodexInteractiveLiveBinding)
}
