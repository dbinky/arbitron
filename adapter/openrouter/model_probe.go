package openrouter

import "errors"

var errModelExecutionEvidence = errors.New("openrouter: missing or mismatched model execution evidence")

// verifyExecutedModel accepts only the exact executable spelling. No alias,
// routing-prefix or deployment equivalence is inferred from similar strings.
// Upstream-controlled model values are intentionally absent from errors.
func verifyExecutedModel(expected, actual string) error {
	if expected == "" || actual == "" || expected != actual {
		return errModelExecutionEvidence
	}
	return nil
}
