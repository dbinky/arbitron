package provider

import (
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// InteractiveStreamSelector implements engine.InteractiveStreamSelector: it
// routes a resolved binding to the appropriate streaming ChatProvider based on
// the binding's Kind:
//   - ProfileKindChat  → delegates to the injected ChatProviderSelector (tool-suppressed stream).
//   - ProfileKindAgent → returns the injected agentStream ChatProvider (tool-enabled stream).
//     Only Provider "claude-cli" (or empty subscription path) is supported in MVP.
type InteractiveStreamSelector struct {
	chat        engine.ChatProviderSelector
	agentStream engine.ChatProvider
}

var _ engine.InteractiveStreamSelector = (*InteractiveStreamSelector)(nil)

// NewInteractiveStreamSelector constructs an InteractiveStreamSelector.
// chat is the existing ChatProviderSelector used for chat-kind bindings.
// agentStream is the tool-enabled streaming adapter (e.g. claudecli.StreamAgentAdapter)
// used for agent-kind bindings.
func NewInteractiveStreamSelector(chat engine.ChatProviderSelector, agentStream engine.ChatProvider) *InteractiveStreamSelector {
	return &InteractiveStreamSelector{chat: chat, agentStream: agentStream}
}

// StreamProviderFor selects the streaming ChatProvider for the given binding.
func (s *InteractiveStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	switch b.Kind {
	case provider.ProfileKindChat:
		return s.chat.ChatProviderFor(b)
	case provider.ProfileKindAgent:
		if b.Provider != "claude-cli" && b.Provider != "" {
			return nil, fmt.Errorf("interactive_agent: provider %q not supported (claude-cli only in MVP)", b.Provider)
		}
		return s.agentStream, nil
	default:
		return nil, fmt.Errorf("interactive stream: unknown profile kind %q", b.Kind)
	}
}
