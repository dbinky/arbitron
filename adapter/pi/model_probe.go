package pi

import (
	"errors"
	"strings"
	"unicode"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelEvidence means the completed stream did not prove execution on the
// requested provider and model. A successful exit or request echo is not proof.
var ErrModelEvidence = errors.New("pi: missing or mismatched model execution evidence")

type executedModel struct {
	provider string
	model    string
}

// verifyExecutedModel consumes the same message_end fold as normal execution.
// D5 requires exact provider/model fields on assistant messages; transcript
// copies and deltas cannot satisfy this check. Pi's catalog is not consulted.
func (out *parsed) verifyExecutedModel(binding provider.ResolvedBinding) error {
	if provider.IsLunaRoute(binding.Provider) && out.lastStopReason != "stop" && out.lastStopReason != "length" {
		return ErrModelEvidence
	}
	expectedProvider := binding.Provider
	if expectedProvider == provider.CanonicalAnthropicAPI {
		expectedProvider = "anthropic"
	}
	if expectedProvider == noConnectionProvider {
		expectedProvider, _, _ = strings.Cut(binding.Model, "/")
		descriptor, ok := providerDescriptors[expectedProvider]
		if !ok || !descriptor.oauth {
			return ErrModelEvidence
		}
	}
	expectedModel := strings.TrimPrefix(strings.TrimSpace(binding.Model), expectedProvider+"/")
	if expectedProvider == "" || expectedModel == "" || len(out.executedModels) == 0 {
		return ErrModelEvidence
	}
	for _, actual := range out.executedModels {
		if provider.IsLunaRoute(binding.Provider) {
			if !safeModelIdentity(actual.model) || actual.provider != "" && (!safeModelIdentity(actual.provider) || actual.provider != provider.CanonicalLunaRoute) ||
				!modelidentity.LunaRouteExecutedModelAccepted(binding.Model, actual.model) {
				return ErrModelEvidence
			}
		} else if actual.provider != expectedProvider || actual.model != expectedModel {
			return ErrModelEvidence
		}
	}
	return nil
}

const maxModelIdentityBytes = 256

func safeModelIdentity(s string) bool {
	return s != "" && len(s) <= maxModelIdentityBytes && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) && provider.RedactLunaRoute(s) == s
}

func (out *parsed) modelEvidence() []engine.ModelEvidence {
	evidence := make([]engine.ModelEvidence, 0, len(out.executedModels))
	for _, m := range out.executedModels {
		evidence = append(evidence, engine.ModelEvidence{Model: m.model, Provider: m.provider, Source: "pi_message_end"})
	}
	return evidence
}
