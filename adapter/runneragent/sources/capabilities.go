package sources

// ClaudeCodeTools is the exact built-in tool vocabulary a claude-code source
// advertises (sorted). The claudecli adapter forwards these names verbatim to
// --allowedTools; pattern forms such as Bash(gh:*) are satisfied by "Bash".
var ClaudeCodeTools = []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch", "Write"}
