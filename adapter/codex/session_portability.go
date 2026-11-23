package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
)

var (
	_ engine.SessionPortability = (*SessionPortability)(nil)
	_ engine.LossyImporter      = (*SessionPortability)(nil)
)

// SessionPortabilityOptions configures the Codex session adapter.
type SessionPortabilityOptions struct {
	Env            []string
	HarnessVersion func(ctx context.Context) (string, error)
	Now            func() time.Time
}

// SessionPortability exports and imports Codex rollouts. Reasoning items are
// ALWAYS stripped on import: encrypted reasoning is org-bound and a foreign
// org fails resume with a hard 400, and no account fingerprint proves the
// accounts match (RR-14).
type SessionPortability struct {
	env     []string
	version func(ctx context.Context) (string, error)
	now     func() time.Time
}

// NewSessionPortability builds the adapter; a version source is required.
func NewSessionPortability(o SessionPortabilityOptions) (*SessionPortability, error) {
	if o.HarnessVersion == nil {
		return nil, errors.New("codex: session portability requires a harness version source")
	}
	env := o.Env
	if env == nil {
		env = codexEnv()
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &SessionPortability{env: env, version: o.HarnessVersion, now: now}, nil
}

// AlwaysLossy implements engine.LossyImporter: every Codex move strips reasoning.
func (*SessionPortability) AlwaysLossy() bool { return true }

func (p *SessionPortability) home() (string, error) {
	home, err := resolveCodexHome(p.env)
	if err != nil {
		return "", errors.New("codex: cannot resolve codex home (no CODEX_HOME or HOME)")
	}
	return home, nil
}

// Export implements engine.SessionPortability: the rollout's raw bytes.
func (p *SessionPortability) Export(ctx context.Context, sessionID, workDir string) (engine.SessionBundle, error) {
	if !threadIDPattern.MatchString(sessionID) {
		return engine.SessionBundle{}, errors.New("codex: malformed thread id")
	}
	if workDir == "" || !filepath.IsAbs(workDir) {
		return engine.SessionBundle{}, errors.New("codex: session workdir must be an absolute path")
	}
	home, err := p.home()
	if err != nil {
		return engine.SessionBundle{}, err
	}
	path, err := findRollout(home, sessionID)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("codex: export session: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("codex: export session: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRolloutBytes+1))
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("codex: export session: %w", err)
	}
	if len(data) > maxRolloutBytes {
		return engine.SessionBundle{}, fmt.Errorf("codex: rollout exceeds %d bytes", maxRolloutBytes)
	}
	ev, err := readRolloutFrom(bytes.NewReader(data), maxRolloutBytes, maxRolloutRecordBytes)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("codex: export session: %w", err)
	}
	if ev.ThreadID != sessionID {
		return engine.SessionBundle{}, errors.New("codex: rollout belongs to another thread")
	}
	version, err := p.version(ctx)
	if err != nil || strings.TrimSpace(version) == "" {
		return engine.SessionBundle{}, fmt.Errorf("codex: harness version unavailable: %v", err)
	}
	return engine.SessionBundle{
		Backend: workflow.BackendCodex, SessionID: sessionID, HarnessVersion: strings.TrimSpace(version),
		OriginWorkDir: workDir, Checksum: engine.BundleChecksum(data), Data: data,
	}, nil
}

// Import refuses every bundle until Cycle 2 implements stripping.
func (p *SessionPortability) Import(ctx context.Context, b engine.SessionBundle, workDir string) (string, error) {
	return "", engine.ErrBundleRequiresRebuild
}
