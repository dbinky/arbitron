// Package inventory detects the server host's harnesses and connections for
// the built-in runner's source set (runner.BuiltInSources).
package inventory

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// ConnectionLister lists connection metadata (no key material).
type ConnectionLister interface {
	List(ctx context.Context) ([]provider.Connection, error)
}

// Config wires detection. Runner defaults to the os/exec runner (never a test
// fake from ModelDependencies); LookPath defaults to exec.LookPath.
type Config struct {
	ClaudeBinary string
	Codex        codex.Config
	Runner       claudecli.CommandRunner
	Connections  ConnectionLister
	LookPath     func(string) (string, error)
}

var ErrNoConnectionLister = errors.New("inventory: a connection lister is required")

// DetectLocal reports harness presence/versions and the current connections.
func DetectLocal(ctx context.Context, cfg Config) (runner.InventoryInput, error) {
	if cfg.Connections == nil {
		return runner.InventoryInput{}, ErrNoConnectionLister
	}
	r := cfg.Runner
	if r == nil {
		r = claudecli.NewExecRunner()
	}
	look := cfg.LookPath
	if look == nil {
		look = exec.LookPath
	}
	var in runner.InventoryInput
	if path, found := claudecli.ResolveBinary(cfg.ClaudeBinary); found {
		p := &runner.HarnessPresence{Path: path}
		if v, err := claudecli.ProbeVersion(ctx, r, path); err != nil {
			p.Reason = "claude --version failed"
		} else {
			p.Version = v
			p.Supported, p.Reason = claudecli.CheckVersion(v)
		}
		in.ClaudeCLI = p
	}
	codexCfg := cfg.Codex
	if codexCfg.Runner == nil {
		codexCfg.Runner = r
	}
	v, ok, reason, err := codex.VersionEvidence(ctx, codexCfg)
	switch {
	case errors.Is(err, codex.ErrCodexMissing):
	case err != nil:
		return in, fmt.Errorf("inventory: codex version: %w", err)
	default:
		path := codexCfg.BinaryPath
		if path == "" {
			path = "codex"
		}
		in.Codex = &runner.HarnessPresence{Path: path, Version: v, Supported: ok, Reason: reason}
	}
	for _, b := range []string{"opencode", "pi"} {
		if _, err := look(b); err == nil {
			in.LocalBackends = append(in.LocalBackends, b)
		}
	}
	conns, err := cfg.Connections.List(ctx)
	if err != nil {
		return runner.InventoryInput{}, fmt.Errorf("inventory: list connections: %w", err)
	}
	in.Connections = conns
	return in, nil
}
