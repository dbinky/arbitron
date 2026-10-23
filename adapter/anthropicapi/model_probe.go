package anthropicapi

import "errors"

var errModelExecutionEvidence = errors.New("anthropicapi: missing or mismatched model execution evidence")

// messageIdentity is reported by the assistant response (or message_start),
// never by a request echo or a content/tool block. Only exact executable
// spelling is accepted; no snapshot, prefix or alias equivalence is inferred.
type messageIdentity struct {
	Type  string `json:"type"`
	Role  string `json:"role"`
	Model string `json:"model"`
}

func (m *messageIdentity) verify(expected string) error {
	if m == nil || expected == "" || m.Type != "message" || m.Role != "assistant" || m.Model != expected {
		// Do not expose upstream-controlled model/role values or credentials.
		return errModelExecutionEvidence
	}
	return nil
}
