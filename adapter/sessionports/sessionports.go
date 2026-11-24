// Package sessionports links harness session portability adapters into one
// registry for the server's built-in runner and the remote runner binary.
package sessionports

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/workflow"
)

// VersionFunc reports the local harness version a bundle is stamped with.
type VersionFunc = func(context.Context) (string, error)

// ClaudeVersion probes <bin> --version.
func ClaudeVersion(r claudecli.CommandRunner, bin string) VersionFunc {
	return func(ctx context.Context) (string, error) {
		if strings.TrimSpace(bin) == "" {
			return "", errors.New("sessionports: claude binary not found")
		}
		return claudecli.ProbeVersion(ctx, r, bin)
	}
}

// InventoryVersion reads one harness version from local inventory detection.
func InventoryVersion(detect func(context.Context) (runner.InventoryInput, error), pick func(runner.InventoryInput) *runner.HarnessPresence, name string) VersionFunc {
	return func(ctx context.Context) (string, error) {
		in, err := detect(ctx)
		if err != nil {
			return "", fmt.Errorf("sessionports: detect %s: %w", name, err)
		}
		p := pick(in)
		if p == nil || strings.TrimSpace(p.Version) == "" {
			return "", fmt.Errorf("sessionports: %s harness not found", name)
		}
		return strings.TrimSpace(p.Version), nil
	}
}

// MapVersion reads one harness version from the runner's source inventory.
func MapVersion(versions func() map[string]string, key string) VersionFunc {
	return func(context.Context) (string, error) {
		v := strings.TrimSpace(versions()[key])
		if v == "" {
			return "", fmt.Errorf("sessionports: %s harness not detected on this runner", key)
		}
		return v, nil
	}
}

// New registers the Claude Code and Codex session adapters.
func New(claudeVersion, codexVersion VersionFunc) (*runner.SessionPortabilityRegistry, error) {
	if claudeVersion == nil || codexVersion == nil {
		return nil, errors.New("sessionports: claude and codex harness version funcs are required")
	}
	claudePort, err := claudecli.NewSessionPortability(claudecli.SessionPortabilityOptions{HarnessVersion: claudeVersion})
	if err != nil {
		return nil, fmt.Errorf("sessionports: claude: %w", err)
	}
	codexPort, err := codex.NewSessionPortability(codex.SessionPortabilityOptions{HarnessVersion: codexVersion})
	if err != nil {
		return nil, fmt.Errorf("sessionports: codex: %w", err)
	}
	return runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{
		workflow.BackendClaudeCode: claudePort,
		workflow.BackendCodex:      codexPort,
	}), nil
}
