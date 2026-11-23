package claudecli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureSID  = "c623adbe-4edc-468c-9256-0157d5025a02"
	fixtureWork = "/srv/arbitron/workspaces/run-1"
)

func versionFunc(v string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return v, nil }
}

func newPortability(t *testing.T, env []string, version string) *SessionPortability {
	t.Helper()
	p, err := NewSessionPortability(SessionPortabilityOptions{Env: env, HarnessVersion: versionFunc(version)})
	require.NoError(t, err)
	return p
}

// installFixture copies testdata/sessions/<name> into <configDir>/projects/<encoded dir>/
// replacing placeholders, and returns the transcript bytes as installed.
func installFixture(t *testing.T, configDir, encodedDir, name string) []byte {
	t.Helper()
	projectDir := filepath.Join(configDir, "projects", encodedDir)
	src := filepath.Join("testdata", "sessions", name)
	fill := func(b []byte) []byte {
		s := strings.ReplaceAll(string(b), "{{PROJECT_DIR}}", projectDir)
		return []byte(strings.ReplaceAll(s, "{{SESSION_ID}}", fixtureSID))
	}
	raw, err := os.ReadFile(filepath.Join(src, "session.jsonl"))
	require.NoError(t, err)
	transcript := fill(raw)
	require.NoError(t, os.MkdirAll(projectDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, fixtureSID+".jsonl"), transcript, 0o600))
	sidecar := filepath.Join(src, "sidecar")
	if _, err := os.Stat(sidecar); err == nil {
		require.NoError(t, filepath.WalkDir(sidecar, func(path string, d os.DirEntry, err error) error {
			require.NoError(t, err)
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(sidecar, path)
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			dst := filepath.Join(projectDir, fixtureSID, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o700))
			return os.WriteFile(dst, fill(b), 0o600)
		}))
	}
	return transcript
}

func untar(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = b
	}
}

func TestEncodeProjectDir(t *testing.T) {
	assert.Equal(t, "-Users-alice-work", EncodeProjectDir("/Users/alice/work"))
	assert.Equal(t, "-Users-a-b-my-repo", EncodeProjectDir("/Users/a.b/my_repo"))
	assert.Equal(t, "-srv-arbitron-workspaces-run-1", EncodeProjectDir(fixtureWork))
}

func TestNewSessionPortability_RequiresVersionSource(t *testing.T) {
	_, err := NewSessionPortability(SessionPortabilityOptions{})
	assert.Error(t, err)
}

func TestClaudeExport_BundlesBytesUnmodified(t *testing.T) {
	home := t.TempDir()
	transcript := installFixture(t, filepath.Join(home, ".claude"), EncodeProjectDir(fixtureWork), "sidecar")
	p := newPortability(t, []string{"HOME=" + home}, "2.1.328")

	b, err := p.Export(context.Background(), fixtureSID, fixtureWork)
	require.NoError(t, err)
	assert.Equal(t, "claude-code", b.Backend)
	assert.Equal(t, fixtureSID, b.SessionID)
	assert.Equal(t, "2.1.328", b.HarnessVersion)
	assert.Equal(t, fixtureWork, b.OriginWorkDir)
	require.NoError(t, b.Verify())

	files := untar(t, b.Data)
	assert.Equal(t, transcript, files["session.jsonl"], "transcript bytes travel unmodified")
	assert.Contains(t, string(files["sidecar/tool-results/toolu_01.json"]), `"exitCode":0`)
	assert.Contains(t, string(files["sidecar/subagents/agent-1.jsonl"]), "subagent task")
	assert.Contains(t, string(files["meta.json"]), filepath.Join(home, ".claude", "projects", EncodeProjectDir(fixtureWork)))
}

func TestClaudeExport_RespectsClaudeConfigDir(t *testing.T) {
	home, cfg := t.TempDir(), t.TempDir()
	installFixture(t, cfg, EncodeProjectDir(fixtureWork), "basic")
	p := newPortability(t, []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + cfg}, "2.1.328")
	b, err := p.Export(context.Background(), fixtureSID, fixtureWork)
	require.NoError(t, err)
	assert.Contains(t, string(untar(t, b.Data)["meta.json"]), cfg)
}

func TestClaudeExport_FindsSessionAppendedUnderAnotherProject(t *testing.T) {
	home := t.TempDir()
	installFixture(t, filepath.Join(home, ".claude"), EncodeProjectDir("/srv/other"), "basic")
	p := newPortability(t, []string{"HOME=" + home}, "2.1.328")
	b, err := p.Export(context.Background(), fixtureSID, fixtureWork)
	require.NoError(t, err, "Claude appends to the original project's file when resumed from another cwd")
	assert.Contains(t, string(untar(t, b.Data)["session.jsonl"]), "PERIWINKLE")
}

func TestClaudeExport_Failures(t *testing.T) {
	home := t.TempDir()
	p := newPortability(t, []string{"HOME=" + home}, "2.1.328")
	_, err := p.Export(context.Background(), fixtureSID, fixtureWork)
	assert.Error(t, err, "missing session")
	_, err = p.Export(context.Background(), "../../etc/passwd", fixtureWork)
	assert.Error(t, err, "malformed id never widens the lookup")
	_, err = p.Export(context.Background(), fixtureSID, "relative/dir")
	assert.Error(t, err, "workdir must be absolute")

	noHome := newPortability(t, []string{}, "2.1.328")
	_, err = noHome.Export(context.Background(), fixtureSID, fixtureWork)
	assert.Error(t, err, "no HOME and no CLAUDE_CONFIG_DIR")

	broken, err := NewSessionPortability(SessionPortabilityOptions{Env: []string{"HOME=" + home},
		HarnessVersion: func(context.Context) (string, error) { return "", errors.New("claude not found") }})
	require.NoError(t, err)
	installFixture(t, filepath.Join(home, ".claude"), EncodeProjectDir(fixtureWork), "basic")
	_, err = broken.Export(context.Background(), fixtureSID, fixtureWork)
	assert.Error(t, err, "an unversioned bundle is never produced")
}

func TestClaudeExport_EnforcesSizeCap(t *testing.T) {
	home := t.TempDir()
	installFixture(t, filepath.Join(home, ".claude"), EncodeProjectDir(fixtureWork), "basic")
	p, err := NewSessionPortability(SessionPortabilityOptions{Env: []string{"HOME=" + home}, HarnessVersion: versionFunc("2.1.328"), MaxBytes: 64})
	require.NoError(t, err)
	_, err = p.Export(context.Background(), fixtureSID, fixtureWork)
	assert.ErrorContains(t, err, "exceeds")
}

func exportFrom(t *testing.T, home, fixture, version string) engine.SessionBundle {
	t.Helper()
	installFixture(t, filepath.Join(home, ".claude"), EncodeProjectDir(fixtureWork), fixture)
	b, err := newPortability(t, []string{"HOME=" + home}, version).Export(context.Background(), fixtureSID, fixtureWork)
	require.NoError(t, err)
	return b
}

func TestSessionPortability_MissingVersionOrDiskFailureFailsClosed(t *testing.T) {
	b := exportFrom(t, t.TempDir(), "basic", "2.1.328")
	for _, tc := range []struct{ name, bundleVersion, targetVersion string }{
		{"missing target version", "2.1.328", ""},
		{"missing bundle version", "", "2.1.328"},
		{"both missing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bundle := b
			bundle.HarnessVersion = tc.bundleVersion
			_, err := newPortability(t, []string{"HOME=" + home}, tc.targetVersion).Import(context.Background(), bundle, fixtureWork)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(home, ".claude", "projects"))
			require.True(t, os.IsNotExist(err), "unversioned state must never be installed")
		})
	}
	t.Run("disk failure", func(t *testing.T) {
		cfg := filepath.Join(t.TempDir(), "not-a-directory")
		require.NoError(t, os.WriteFile(cfg, []byte("occupied"), 0o600))
		_, err := newPortability(t, []string{"CLAUDE_CONFIG_DIR=" + cfg}, "2.1.328").Import(context.Background(), b, fixtureWork)
		require.Error(t, err)
		got, err := os.ReadFile(cfg)
		require.NoError(t, err)
		assert.Equal(t, "occupied", string(got))
	})
}

func TestClaudeImport_SameHomeRestoresTranscriptAndSidecars(t *testing.T) {
	home := t.TempDir()
	b := exportFrom(t, home, "sidecar", "2.1.328")
	projectDir := filepath.Join(home, ".claude", "projects", EncodeProjectDir(fixtureWork))
	original, err := os.ReadFile(filepath.Join(projectDir, fixtureSID+".jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(projectDir)) // the runner lost its local copy

	sid, err := newPortability(t, []string{"HOME=" + home}, "2.1.328").Import(context.Background(), b, fixtureWork)
	require.NoError(t, err)
	assert.Equal(t, fixtureSID, sid)
	restored, err := os.ReadFile(filepath.Join(projectDir, fixtureSID+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, original, restored, "bytes are never edited")
	tool, err := os.ReadFile(filepath.Join(projectDir, fixtureSID, "tool-results", "toolu_01.json"))
	require.NoError(t, err)
	assert.Contains(t, string(tool), `"exitCode":0`)
	st, err := os.Stat(filepath.Join(projectDir, fixtureSID+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestClaudeImport_DifferentHomeWithoutAbsoluteRefsInstallsNatively(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	b := exportFrom(t, homeA, "basic", "2.1.328")
	sid, err := newPortability(t, []string{"HOME=" + homeB}, "2.1.328").Import(context.Background(), b, fixtureWork)
	require.NoError(t, err)
	assert.Equal(t, fixtureSID, sid)
	got, err := os.ReadFile(filepath.Join(homeB, ".claude", "projects", EncodeProjectDir(fixtureWork), fixtureSID+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, untar(t, b.Data)["session.jsonl"], got)
}

func TestClaudeImport_DifferentHomeWithAbsoluteRefsRequiresRebuild(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	b := exportFrom(t, homeA, "sidecar", "2.1.328")
	_, err := newPortability(t, []string{"HOME=" + homeB}, "2.1.328").Import(context.Background(), b, fixtureWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild, "RR-13: unverified cross-HOME mapping fails closed to the visible rebuild")
	_, statErr := os.Stat(filepath.Join(homeB, ".claude", "projects", EncodeProjectDir(fixtureWork), fixtureSID+".jsonl"))
	assert.True(t, os.IsNotExist(statErr), "nothing is installed on refusal")
}

func TestClaudeImport_VersionSkewRequiresRebuild(t *testing.T) {
	home := t.TempDir()
	b := exportFrom(t, home, "basic", "2.1.328")
	_, err := newPortability(t, []string{"HOME=" + t.TempDir()}, "2.1.400").Import(context.Background(), b, fixtureWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
}

func TestClaudeImport_DifferentWorkDirRequiresRebuild(t *testing.T) {
	home := t.TempDir()
	b := exportFrom(t, home, "basic", "2.1.328")
	_, err := newPortability(t, []string{"HOME=" + home}, "2.1.328").Import(context.Background(), b, "/srv/arbitron/workspaces/run-2")
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild, "D2: identical workspace paths only")
}

func TestClaudeImport_CorruptBundleRequiresRebuild(t *testing.T) {
	home := t.TempDir()
	b := exportFrom(t, home, "basic", "2.1.328")
	b.Data = append([]byte{}, b.Data...)
	b.Data[len(b.Data)-1] ^= 0xFF
	_, err := newPortability(t, []string{"HOME=" + home}, "2.1.328").Import(context.Background(), b, fixtureWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
	assert.ErrorIs(t, err, engine.ErrBundleChecksum)
}

func TestClaudeImport_RejectsForeignBackendAndPathTraversal(t *testing.T) {
	home := t.TempDir()
	p := newPortability(t, []string{"HOME=" + home}, "2.1.328")
	b := exportFrom(t, home, "basic", "2.1.328")
	b.Backend = "codex"
	_, err := p.Import(context.Background(), b, fixtureWork)
	assert.Error(t, err)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range map[string]string{
		"meta.json":          `{"format":1,"originConfigDir":"/x","originProjectDir":"/x/projects/p"}`,
		"session.jsonl":      `{"type":"user"}`,
		"sidecar/../../evil": "pwned",
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	evil := engine.SessionBundle{Backend: "claude-code", SessionID: fixtureSID, HarnessVersion: "2.1.328", OriginWorkDir: fixtureWork,
		Data: buf.Bytes(), Checksum: engine.BundleChecksum(buf.Bytes())}
	_, err = p.Import(context.Background(), evil, fixtureWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
	_, statErr := os.Stat(filepath.Join(home, ".claude", "evil"))
	assert.True(t, os.IsNotExist(statErr))
}
