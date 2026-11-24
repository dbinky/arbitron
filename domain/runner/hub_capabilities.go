package runner

import (
	"encoding/hex"
	"fmt"
	"slices"
)

const maxRemoteCapabilities = 32
const maxRemoteTools = 256

// RemoteAgentBackends are the native agent backends remote runners can serve.
var RemoteAgentBackends = []string{"claude-code", "codex"}

func remoteCapabilityOp(c ExecutionCapability) bool {
	switch c.Op {
	case OpChat, OpStream, OpConfirm:
		return true
	case OpAgent:
		return slices.Contains(RemoteAgentBackends, c.Backend)
	}
	return false
}

func validateSourceReports(reports []SourceReport) error {
	for _, report := range reports {
		if len(report.Capabilities) > maxRemoteCapabilities {
			return fmt.Errorf("%w: too many source capabilities", ErrMalformedMessage)
		}
		for _, c := range report.Capabilities {
			fingerprint, err := hex.DecodeString(c.EndpointFingerprint)
			if !remoteCapabilityOp(c) || c.Provider == "" || err != nil || len(fingerprint) != 32 || len(c.Families) == 0 || len(c.Families) > maxRemoteCapabilities || len(c.Models) == 0 || len(c.Models) > maxRemoteCapabilities || len(c.ContextWindows) > maxRemoteCapabilities || len(c.Efforts) > maxRemoteCapabilities || len(c.Tools) > maxRemoteTools {
				return fmt.Errorf("%w: invalid remote execution capability", ErrMalformedMessage)
			}
			if c.Op == OpConfirm && c.Backend != "claude-code" && c.Backend != "codex" {
				return fmt.Errorf("%w: confirm capability needs a native verifier backend", ErrMalformedMessage)
			}
			for _, family := range c.Families {
				switch family {
				case "anthropic", "openai", "deepseek", "zhipuai", "moonshotai":
				default:
					return fmt.Errorf("%w: unknown capability family", ErrMalformedMessage)
				}
			}
			for _, model := range c.Models {
				if model == "" {
					return fmt.Errorf("%w: empty capability model", ErrMalformedMessage)
				}
			}
			for _, tool := range c.Tools {
				if tool == "" {
					return fmt.Errorf("%w: empty capability tool", ErrMalformedMessage)
				}
			}
			for _, axis := range c.ContextWindows {
				if axis == "" {
					return fmt.Errorf("%w: empty context window", ErrMalformedMessage)
				}
			}
			for _, axis := range c.Efforts {
				if axis == "" {
					return fmt.Errorf("%w: empty effort", ErrMalformedMessage)
				}
			}
		}
	}
	return nil
}
