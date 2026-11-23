package claudecli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
)

const (
	claudeBundleFormat     = 1
	defaultBundleMaxBytes  = 512 << 20
	bundleMetaName         = "meta.json"
	bundleTranscriptName   = "session.jsonl"
	bundleSidecarPrefix    = "sidecar/"
	claudeConfigDirEnvName = "CLAUDE_CONFIG_DIR"
)

// claudeSessionIDPattern admits only harness-issued ids, so an id can never
// widen a glob or escape the projects directory.
var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{7,63}$`)

var _ engine.SessionPortability = (*SessionPortability)(nil)

// SessionPortabilityOptions configures the Claude Code session adapter.
type SessionPortabilityOptions struct {
	Env            []string
	HarnessVersion func(ctx context.Context) (string, error)
	MaxBytes       int64
}

// SessionPortability exports and imports Claude Code sessions. It never edits
// transcript bytes: signed thinking blocks and prefix-checked history must
// arrive exactly as written.
type SessionPortability struct {
	env      []string
	version  func(ctx context.Context) (string, error)
	maxBytes int64
}

type claudeBundleMeta struct {
	Format           int    `json:"format"`
	OriginConfigDir  string `json:"originConfigDir"`
	OriginProjectDir string `json:"originProjectDir"`
}

// NewSessionPortability builds the adapter. A version source is required: an
// unversioned bundle could never be checked for skew.
func NewSessionPortability(o SessionPortabilityOptions) (*SessionPortability, error) {
	if o.HarnessVersion == nil {
		return nil, errors.New("claudecli: session portability requires a harness version source")
	}
	env := o.Env
	if env == nil {
		env = procenv.BaseEnv()
	}
	max := o.MaxBytes
	if max <= 0 {
		max = defaultBundleMaxBytes
	}
	return &SessionPortability{env: env, version: o.HarnessVersion, maxBytes: max}, nil
}

// EncodeProjectDir is Claude Code's project-folder name for a cwd: every rune
// outside [A-Za-z0-9] becomes '-' ("/Users/alice/work" => "-Users-alice-work").
func EncodeProjectDir(workDir string) string {
	var b strings.Builder
	for _, r := range workDir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// configDir is the directory claude itself uses for this launch env.
func (p *SessionPortability) configDir() (string, error) {
	var home, cfg string
	for _, kv := range p.env {
		switch {
		case strings.HasPrefix(kv, claudeConfigDirEnvName+"="):
			cfg = strings.TrimSpace(strings.TrimPrefix(kv, claudeConfigDirEnvName+"="))
		case strings.HasPrefix(kv, "HOME="):
			home = strings.TrimSpace(strings.TrimPrefix(kv, "HOME="))
		}
	}
	if cfg != "" {
		return cfg, nil
	}
	if home == "" {
		return "", errors.New("claudecli: cannot resolve the claude config dir (no CLAUDE_CONFIG_DIR or HOME)")
	}
	return filepath.Join(home, ".claude"), nil
}

func validateSessionArgs(sessionID, workDir string) error {
	if !claudeSessionIDPattern.MatchString(sessionID) {
		return errors.New("claudecli: malformed session id")
	}
	if workDir == "" || !filepath.IsAbs(workDir) {
		return errors.New("claudecli: session workdir must be an absolute path")
	}
	return nil
}

// locateTranscript finds <projects>/<encoded workDir>/<id>.jsonl, else the
// single <projects>/*/<id>.jsonl (claude resumed from another cwd appends to
// the original project's file). Zero or several matches is an error.
func locateTranscript(projects, sessionID, workDir string) (string, error) {
	primary := filepath.Join(projects, EncodeProjectDir(workDir), sessionID+".jsonl")
	if st, err := os.Stat(primary); err == nil && st.Mode().IsRegular() {
		return primary, nil
	}
	matches, err := filepath.Glob(filepath.Join(projects, "*", sessionID+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("claudecli: locate session: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("claudecli: %d stored transcripts for the session (want exactly 1)", len(matches))
	}
	return matches[0], nil
}

// Export implements engine.SessionPortability.
func (p *SessionPortability) Export(ctx context.Context, sessionID, workDir string) (engine.SessionBundle, error) {
	if err := validateSessionArgs(sessionID, workDir); err != nil {
		return engine.SessionBundle{}, err
	}
	cfg, err := p.configDir()
	if err != nil {
		return engine.SessionBundle{}, err
	}
	transcriptPath, err := locateTranscript(filepath.Join(cfg, "projects"), sessionID, workDir)
	if err != nil {
		return engine.SessionBundle{}, err
	}
	version, err := p.version(ctx)
	if err != nil || strings.TrimSpace(version) == "" {
		return engine.SessionBundle{}, fmt.Errorf("claudecli: harness version unavailable: %v", err)
	}
	projectDir := filepath.Dir(transcriptPath)
	var budget = p.maxBytes
	readCapped := func(path string) ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, budget+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > budget {
			return nil, fmt.Errorf("claudecli: session bundle exceeds %d bytes", p.maxBytes)
		}
		budget -= int64(len(b))
		return b, nil
	}
	transcript, err := readCapped(transcriptPath)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("claudecli: read transcript: %w", err)
	}
	meta, err := json.Marshal(claudeBundleMeta{Format: claudeBundleFormat, OriginConfigDir: cfg, OriginProjectDir: projectDir})
	if err != nil {
		return engine.SessionBundle{}, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := add(bundleMetaName, meta); err != nil {
		return engine.SessionBundle{}, err
	}
	if err := add(bundleTranscriptName, transcript); err != nil {
		return engine.SessionBundle{}, err
	}
	sidecarDir := filepath.Join(projectDir, sessionID)
	if st, err := os.Stat(sidecarDir); err == nil && st.IsDir() {
		walkErr := filepath.WalkDir(sidecarDir, func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("claudecli: sidecar entry %q is not a regular file", d.Name())
			}
			rel, err := filepath.Rel(sidecarDir, fpath)
			if err != nil {
				return err
			}
			body, err := readCapped(fpath)
			if err != nil {
				return err
			}
			return add(bundleSidecarPrefix+filepath.ToSlash(rel), body)
		})
		if walkErr != nil {
			return engine.SessionBundle{}, fmt.Errorf("claudecli: export sidecar: %w", walkErr)
		}
	}
	if err := tw.Close(); err != nil {
		return engine.SessionBundle{}, err
	}
	if err := zw.Close(); err != nil {
		return engine.SessionBundle{}, err
	}
	data := buf.Bytes()
	return engine.SessionBundle{
		Backend: workflow.BackendClaudeCode, SessionID: sessionID, HarnessVersion: strings.TrimSpace(version),
		OriginWorkDir: workDir, Checksum: engine.BundleChecksum(data), Data: data,
	}, nil
}

// Import is added in Cycle 3.
var _ = path.Clean

func (p *SessionPortability) Import(ctx context.Context, b engine.SessionBundle, workDir string) (string, error) {
	return "", engine.ErrBundleRequiresRebuild
}
