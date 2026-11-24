package runnerproto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const maxConfirmationField = 256

// WireConfirmation is the allowlisted native exact-model confirmation
// descriptor. No prompt, credential, path or environment crosses the wire; the
// runner uses its own login and a per-job scratch directory.
type WireConfirmation struct {
	Model               string `json:"model"`
	Backend             string `json:"backend"`
	Provider            string `json:"provider"`
	EndpointFingerprint string `json:"endpointFingerprint,omitempty"`
	ContextWindow       string `json:"contextWindow,omitempty"`
	Effort              string `json:"effort,omitempty"`
}

func (c *WireConfirmation) Validate() error {
	for name, v := range map[string]string{"model": c.Model, "backend": c.Backend, "provider": c.Provider,
		"contextWindow": c.ContextWindow, "effort": c.Effort} {
		if len(v) > maxConfirmationField {
			return fmt.Errorf("confirmation.%s longer than %d bytes", name, maxConfirmationField)
		}
	}
	switch {
	case c.Model == "":
		return errors.New("confirmation.model is required (exact model, never substituted)")
	case c.Backend != "claude-code" && c.Backend != "codex":
		return fmt.Errorf("confirmation.backend %q has no native verifier", c.Backend)
	case c.Provider == "":
		return errors.New("confirmation.provider is required")
	}
	if c.EndpointFingerprint != "" {
		if fp, err := hex.DecodeString(c.EndpointFingerprint); err != nil || len(fp) != sha256.Size {
			return errors.New("confirmation.endpointFingerprint must be SHA-256 hex")
		}
	}
	return nil
}

// WireConfirmationResult is the harness's own exact-model evidence.
type WireConfirmationResult struct {
	RequestedModel string `json:"requestedModel"`
	ExecutedModel  string `json:"executedModel"`
	SessionID      string `json:"sessionId,omitempty"`
}

func (r *WireConfirmationResult) Validate() error {
	if r.RequestedModel == "" || r.ExecutedModel == "" {
		return errors.New("confirmation result needs requested and executed models")
	}
	if len(r.RequestedModel) > maxConfirmationField || len(r.ExecutedModel) > maxConfirmationField || len(r.SessionID) > maxConfirmationField {
		return fmt.Errorf("confirmation result fields are limited to %d bytes", maxConfirmationField)
	}
	return nil
}
