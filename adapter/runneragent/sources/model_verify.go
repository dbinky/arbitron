package sources

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/workflow"
)

// VerifiedDiscover reads current native model evidence on every discovery pass.
func VerifiedDiscover(base DiscoverInput, v *ClaudeModelVerifier) DiscoverFunc {
	return func(ctx context.Context) ([]Spec, map[string]string) {
		in := base
		in.ClaudeModels = v.Verified()
		return Discover(ctx, in)
	}
}

// ClaudeModelVerifier natively confirms the exact Claude models the server
// requests at admission (contracts D1). Only verified models are advertised.
type ClaudeModelVerifier struct {
	confirm runner.ConfirmationExecutor
	workDir string
	log     *slog.Logger

	mu        sync.Mutex
	requested []string
	verified  map[string]bool
	refused   map[string]bool
}

func NewClaudeModelVerifier(c runner.ConfirmationExecutor, workDir string, log *slog.Logger) *ClaudeModelVerifier {
	if c == nil {
		panic("sources: NewClaudeModelVerifier requires a confirmation executor")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ClaudeModelVerifier{confirm: c, workDir: workDir, log: log, verified: map[string]bool{}, refused: map[string]bool{}}
}

// Request replaces the requested set and confirms every uncached model, one
// at a time. It returns when all are checked or ctx ends.
func (v *ClaudeModelVerifier) Request(ctx context.Context, models []string) {
	v.mu.Lock()
	v.requested = append([]string(nil), models...)
	v.mu.Unlock()
	for _, m := range models {
		if ctx.Err() != nil {
			return
		}
		v.mu.Lock()
		cached := v.verified[m] || v.refused[m]
		v.mu.Unlock()
		if cached {
			continue
		}
		res, err := v.confirm.Confirm(ctx, provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: claudeChatProvider, Backend: workflow.BackendClaudeCode, Model: m}, v.workDir)
		if err == nil {
			err = runner.VerifyConfirmation(m, &res)
		}
		v.mu.Lock()
		switch {
		case err == nil:
			v.verified[m] = true
		case errors.Is(err, runner.ErrConfirmationModelMismatch):
			v.refused[m] = true
			v.log.Warn("claude model not advertised: native confirmation did not prove the exact model", "model", m, "error_class", "model_mismatch")
		default:
			v.log.Warn("claude model not advertised: native confirmation failed; retried at next admission", "model", m, "error_class", "confirm_failed")
		}
		v.mu.Unlock()
	}
}

// Verified returns the sorted requested models that verified.
func (v *ClaudeModelVerifier) Verified() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, m := range v.requested {
		if v.verified[m] && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}
