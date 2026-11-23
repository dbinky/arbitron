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

func rebuild(format string, args ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrBundleRequiresRebuild, fmt.Sprintf(format, args...))
}

type claudeBundleContents struct {
	meta       claudeBundleMeta
	transcript []byte
	sidecar    map[string][]byte // slash-relative path under <id>/
}

// readClaudeBundle unpacks Data, refusing unsafe names and unexpected entries.
func readClaudeBundle(data []byte, max int64) (claudeBundleContents, error) {
	var out claudeBundleContents
	out.sidecar = map[string][]byte{}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return out, rebuild("unreadable bundle: %v", err)
	}
	tr := tar.NewReader(zr)
	var total int64
	var sawMeta, sawTranscript bool
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, rebuild("unreadable bundle: %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			return out, rebuild("bundle entry %q is not a regular file", h.Name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, max-total+1))
		if err != nil {
			return out, rebuild("unreadable bundle entry: %v", err)
		}
		total += int64(len(body))
		if total > max {
			return out, rebuild("bundle exceeds %d bytes", max)
		}
		switch {
		case h.Name == bundleMetaName:
			if err := json.Unmarshal(body, &out.meta); err != nil || out.meta.Format != claudeBundleFormat || out.meta.OriginProjectDir == "" {
				return out, rebuild("unsupported bundle metadata")
			}
			sawMeta = true
		case h.Name == bundleTranscriptName:
			out.transcript, sawTranscript = body, true
		case strings.HasPrefix(h.Name, bundleSidecarPrefix):
			rel := strings.TrimPrefix(h.Name, bundleSidecarPrefix)
			clean := path.Clean(rel)
			if rel == "" || clean != rel || strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) {
				return out, rebuild("unsafe sidecar path %q", h.Name)
			}
			out.sidecar[clean] = body
		default:
			return out, rebuild("unexpected bundle entry %q", h.Name)
		}
	}
	if !sawMeta || !sawTranscript {
		return out, rebuild("bundle is missing its metadata or transcript")
	}
	return out, nil
}

// Import implements engine.SessionPortability. It installs the transcript and
// sidecars byte-for-byte under the target's project dir for the SAME workDir.
func (p *SessionPortability) Import(ctx context.Context, b engine.SessionBundle, workDir string) (string, error) {
	if b.Backend != workflow.BackendClaudeCode {
		return "", fmt.Errorf("claudecli: cannot import a %q bundle", b.Backend)
	}
	if err := validateSessionArgs(b.SessionID, workDir); err != nil {
		return "", err
	}
	if err := b.Verify(); err != nil {
		return "", fmt.Errorf("%w: %w", engine.ErrBundleRequiresRebuild, err)
	}
	version, err := p.version(ctx)
	if err != nil {
		return "", fmt.Errorf("claudecli: harness version unavailable: %w", err)
	}
	if strings.TrimSpace(version) == "" || strings.TrimSpace(b.HarnessVersion) == "" {
		return "", rebuild("harness version is missing")
	}
	if strings.TrimSpace(version) != b.HarnessVersion {
		return "", rebuild("harness version skew (bundle %s, target %s)", b.HarnessVersion, strings.TrimSpace(version))
	}
	if workDir != b.OriginWorkDir {
		return "", rebuild("workspace path differs from the origin (identical paths are required)")
	}
	contents, err := readClaudeBundle(b.Data, p.maxBytes)
	if err != nil {
		return "", err
	}
	cfg, err := p.configDir()
	if err != nil {
		return "", err
	}
	projectDir := filepath.Join(cfg, "projects", EncodeProjectDir(workDir))
	if filepath.Clean(projectDir) != filepath.Clean(contents.meta.OriginProjectDir) {
		// RR-13: absolute tool-result/sidecar references would point at the
		// origin HOME. Rewriting signed content is unsafe; refuse natively.
		origin := []byte(contents.meta.OriginProjectDir)
		if bytes.Contains(contents.transcript, origin) {
			return "", rebuild("transcript references the origin session store by absolute path")
		}
		for name, body := range contents.sidecar {
			if bytes.Contains(body, origin) {
				return "", rebuild("sidecar %q references the origin session store by absolute path", name)
			}
		}
	}
	if err := installClaudeSession(projectDir, b.SessionID, contents); err != nil {
		return "", fmt.Errorf("claudecli: install session: %w", err)
	}
	return b.SessionID, nil
}

// installClaudeSession writes the sidecar tree to a temp dir and swaps it in,
// then writes the transcript via temp file + rename, so a crash never leaves a
// half-written transcript in place.
func installClaudeSession(projectDir, sessionID string, c claudeBundleContents) error {
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(projectDir, "."+sessionID+".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for rel, body := range c.sidecar {
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o600); err != nil {
			return err
		}
	}
	sidecarDir := filepath.Join(projectDir, sessionID)
	if err := os.RemoveAll(sidecarDir); err != nil {
		return err
	}
	if len(c.sidecar) > 0 {
		if err := os.Rename(staging, sidecarDir); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(projectDir, "."+sessionID+".jsonl-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(c.transcript); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, filepath.Join(projectDir, sessionID+".jsonl"))
}
