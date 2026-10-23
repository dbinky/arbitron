package pi

import (
	"errors"
	"strings"

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
		if actual.provider != expectedProvider || actual.model != expectedModel {
			return ErrModelEvidence
		}
	}
	return nil
}
