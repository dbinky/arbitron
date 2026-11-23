package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

func rebuild(format string, args ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrBundleRequiresRebuild, fmt.Sprintf(format, args...))
}

// stripRollout returns the rollout without reasoning items or any
// encrypted_content, refusing compaction state. Every record must be JSON with
// a type; the session_meta id must equal threadID.
func stripRollout(data []byte, threadID string) ([]byte, error) {
	if len(data) > maxRolloutBytes {
		return nil, rebuild("rollout exceeds %d bytes", maxRolloutBytes)
	}
	br := bufio.NewReader(bytes.NewReader(data))
	var out bytes.Buffer
	sawMeta := false
	for {
		line, err := readRolloutRecord(br, maxRolloutRecordBytes)
		if err == errRolloutRecordTooLarge {
			return nil, rebuild("rollout record exceeds %d bytes", maxRolloutRecordBytes)
		}
		if err != nil && err != io.EOF {
			return nil, rebuild("unreadable rollout")
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			kept, keep, perr := stripRecord(trimmed, threadID, &sawMeta)
			if perr != nil {
				return nil, perr
			}
			if keep {
				out.Write(kept)
				out.WriteByte('\n')
			}
		}
		if err == io.EOF {
			break
		}
	}
	if !sawMeta {
		return nil, rebuild("rollout has no session_meta")
	}
	return out.Bytes(), nil
}

// hasReplacementHistory checks decoded keys, including escaped keys and nested
// payloads, rather than relying on the spelling of untrusted JSON bytes.
func hasReplacementHistory(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		for key, value := range obj {
			if key == "replacement_history" || hasReplacementHistory(value) {
				return true
			}
		}
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		for _, item := range items {
			if hasReplacementHistory(item) {
				return true
			}
		}
	}
	return false
}

func stripRecord(line []byte, threadID string, sawMeta *bool) ([]byte, bool, error) {
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, false, rebuild("malformed rollout record")
	}
	var typ string
	if err := json.Unmarshal(rec["type"], &typ); err != nil || typ == "" {
		return nil, false, rebuild("rollout record without a type")
	}
	if typ == "compacted" || typ == "compaction" || hasReplacementHistory(line) {
		return nil, false, rebuild("rollout contains compaction state")
	}
	var payload map[string]json.RawMessage
	if raw, ok := rec["payload"]; ok && len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, false, rebuild("malformed rollout payload")
		}
	}
	var ptype string
	if payload != nil {
		_ = json.Unmarshal(payload["type"], &ptype)
	}
	switch {
	case typ == "session_meta":
		var id string
		if err := json.Unmarshal(payload["id"], &id); err != nil || id != threadID {
			return nil, false, rebuild("rollout session_meta does not name the bundle's thread")
		}
		*sawMeta = true
	case typ == "response_item" && ptype == "reasoning":
		return nil, false, nil // dropped whole: org-bound, and store=false ids cannot be dereferenced
	case typ == "response_item" && (ptype == "compaction" || ptype == "compaction_summary"):
		return nil, false, rebuild("rollout contains compaction state")
	}
	if payload != nil {
		if _, has := payload["encrypted_content"]; has {
			delete(payload, "encrypted_content")
			newPayload, err := json.Marshal(payload)
			if err != nil {
				return nil, false, err
			}
			rec["payload"] = newPayload
			rewritten, err := json.Marshal(rec)
			if err != nil {
				return nil, false, err
			}
			return rewritten, true, nil
		}
	}
	return line, true, nil
}

// Import implements engine.SessionPortability.
func (p *SessionPortability) Import(ctx context.Context, b engine.SessionBundle, workDir string) (string, error) {
	if b.Backend != workflow.BackendCodex {
		return "", fmt.Errorf("codex: cannot import a %q bundle", b.Backend)
	}
	if !threadIDPattern.MatchString(b.SessionID) {
		return "", errors.New("codex: malformed thread id")
	}
	if err := b.Verify(); err != nil {
		return "", fmt.Errorf("%w: %w", engine.ErrBundleRequiresRebuild, err)
	}
	version, err := p.version(ctx)
	if err != nil {
		return "", fmt.Errorf("codex: harness version unavailable: %w", err)
	}
	if strings.TrimSpace(version) == "" || strings.TrimSpace(b.HarnessVersion) == "" || strings.TrimSpace(version) != b.HarnessVersion {
		return "", rebuild("harness version skew (bundle %s, target %s)", b.HarnessVersion, strings.TrimSpace(version))
	}
	if workDir != b.OriginWorkDir {
		return "", rebuild("workspace path differs from the origin (identical paths are required)")
	}
	stripped, err := stripRollout(b.Data, b.SessionID)
	if err != nil {
		return "", err
	}
	home, err := p.home()
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+b.SessionID+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("codex: locate existing rollout: %w", err)
	}
	var target string
	switch len(matches) {
	case 0:
		now := p.now().UTC()
		target = filepath.Join(home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"),
			"rollout-"+now.Format("2006-01-02T15-04-05")+"-"+b.SessionID+".jsonl")
	case 1:
		target = matches[0]
	default:
		return "", fmt.Errorf("codex: %d rollouts already stored for the thread", len(matches))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".import-"+b.SessionID+"-")
	if err != nil {
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(stripped); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("codex: install rollout: %w", err)
	}
	return b.SessionID, nil
}
