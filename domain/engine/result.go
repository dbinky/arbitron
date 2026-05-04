package engine

// NodeResult captures the output and metadata from executing a single node.
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
}
