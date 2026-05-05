package engine

// NodeResult captures the output and metadata from executing a single node.
//
// Field-to-executor mapping:
//
//	Output          — All executors. The primary result value stored in the variable store.
//	OutputPort      — ConditionalExecutor. Names the edge to follow (e.g., "high", "low").
//	LLMConversation — LLMCallExecutor. The full message history from the LLM call.
//	LLMModel        — LLMCallExecutor. The model identifier used for the call.
//	LLMTokensIn     — LLMCallExecutor. Input token count reported by the provider.
//	LLMTokensOut    — LLMCallExecutor. Output token count reported by the provider.
//	ScriptStdout    — ScriptExecutor, ToolCallExecutor. Captured stdout from the process.
//	ScriptStderr    — ScriptExecutor, ToolCallExecutor. Captured stderr from the process.
//	ScriptExitCode  — ScriptExecutor, ToolCallExecutor. Non-zero exit code on failure.
type NodeResult struct {
	Output          any
	OutputPort      string
	LLMConversation any
	LLMModel        string
	LLMTokensIn     int
	LLMTokensOut    int
	ScriptStdout    string
	ScriptStderr    string
	ScriptExitCode  int
	// IsWait signals that the engine should exit the run loop cleanly because
	// the instance has transitioned to a waiting state. The executor has already
	// handled the state transition.
	IsWait bool
}
