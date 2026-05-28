package engine

import "github.com/arbitron/arbitron/domain/variable"

// Engine-reserved variable names. These keys are seeded by the engine itself
// (not by workflow authors) and are surfaced here as constants so callers
// don't repeat the literal string across files. The variable package's
// ReservedInputNames map is the runtime-enforcement source of truth.
//
// SECURITY: closes the stringly-typed-map leak called out in PR #8 thread
// 3291907743. Five callers previously hard-coded "workspace"; consolidating
// the literal here makes any future rename a single-point change and matches
// the WorkDirFromVars helper below so executors stop digging into the
// variable store directly.
const ReservedVarWorkspace = "workspace"

// LLMConversationKey* are the well-known keys inside the LLMConversation
// map persisted on a NodeExecution. PR #8 thread 3291907742 — closes the
// stringly-typed map access for the claude-cli session-resumption contract.
const (
	ConversationKeySessionID = "sessionId"
	ConversationKeyNonce     = "nonce"
	ConversationKeyMessages  = "messages"
)

// WorkDirFromVars returns the seeded workspace path from the variable store.
// The second return distinguishes "not seeded at all" (false) from "seeded
// as empty string" (true) — load-bearing for the claudecli provider's
// refusal to run with an empty WorkDir.
func WorkDirFromVars(vars *variable.VariableStore) (string, bool) {
	if vars == nil {
		return "", false
	}
	raw, ok := vars.Get(ReservedVarWorkspace)
	if !ok {
		return "", false
	}
	s, _ := raw.(string)
	return s, true
}

// SessionID extracts the persisted claude session id from an LLMConversation
// map. Empty when no session is stored.
func SessionID(conv map[string]any) string {
	if conv == nil {
		return ""
	}
	id, _ := conv[ConversationKeySessionID].(string)
	return id
}

// SetSessionID stores sessionID under the canonical key on conv. The map is
// returned for chaining; a nil conv allocates a new map.
func SetSessionID(conv map[string]any, sessionID string) map[string]any {
	if conv == nil {
		conv = map[string]any{}
	}
	conv[ConversationKeySessionID] = sessionID
	return conv
}
