package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/workflow"
)

// HarnessFingerprinter returns the unkeyed bytes describing the on-disk inputs
// a harness backend runs from: its binary, account and static configuration
// files. The inventory passes them through the keyed fingerprint, so nothing
// here is exposed in clear. A backend with no harness (chat, unknown) returns nil bytes; an input
// that exists but cannot be read is an error, never a partial fingerprint.
type HarnessFingerprinter func(backend string) ([]byte, error)

// HarnessFilesConfig locates the harness inputs. Zero values resolve like the
// harness adapters do: an explicit binary path, else PATH, else the standard
// bin dirs; files under the user's home, CODEX_HOME or CLAUDE_CONFIG_DIR,
// matching the environment forwarded by the launchers.
type HarnessFilesConfig struct {
	Home string
	// Binaries maps a backend to an explicit binary path ("" or missing means
	// search for the backend's default binary name).
	Binaries map[string]string
	Getenv   func(string) string
	// ProjectDir is the working directory the discovery probes launch the
	// harness from. Claude reads project-scope settings and MCP servers there,
	// so they bind the revision like the user-scope files ("" adds none).
	ProjectDir string
}

// NewHarnessFileFingerprinter fingerprints binaries by path, size and mtime
// (they are large) and account/config files by content, projecting OAuth
// identity and ignoring known renewal/counter fields. A configuration change,
// account switch or binary upgrade invalidates proof; growing session output
// does not. Sensitive digests are consumed only by the inventory's keyed hash.
func NewHarnessFileFingerprinter(cfg HarnessFilesConfig) HarnessFingerprinter {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return func(backend string) ([]byte, error) {
		binName, files := harnessInputs(backend, cfg.Home, getenv)
		if backend == workflow.BackendClaudeCode && cfg.ProjectDir != "" {
			files = append(files, claudeProjectInputs(cfg.ProjectDir)...)
		}
		if binName == "" {
			return nil, nil
		}
		var out bytes.Buffer
		bin := cfg.Binaries[backend]
		if bin == "" {
			bin = findBinary(binName)
		}
		if err := fingerprintFile(&out, "binary", bin, false, nil); err != nil {
			return nil, err
		}
		for _, f := range files {
			var project func(io.Reader) ([]byte, error)
			if backend == workflow.BackendClaudeCode {
				switch filepath.Base(f) {
				case ".credentials.json":
					project = claudeCredentialProjection
				case ".claude.json":
					project = claudeGlobalProjection(cfg.ProjectDir)
				}
			}
			if backend == workflow.BackendCodex {
				switch filepath.Base(f) {
				case "auth.json":
					project = codexAccountProjection
				case "models_cache.json":
					project = codexCacheProjection
				}
			}
			if err := fingerprintFile(&out, "config", f, true, project); err != nil {
				return nil, err
			}
		}
		return out.Bytes(), nil
	}
}

// harnessInputs names global inputs used by the actual controlled launch env.
// Pi and OpenCode receive HOME via BaseEnv, not ambient PI_CODING_AGENT_DIR or
// XDG_* overrides. Their generated per-binding config is fingerprinted by the
// inventory itself. Do not include session/rollout directories here.
func harnessInputs(backend, home string, getenv func(string) string) (string, []string) {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	switch backend {
	case workflow.BackendClaudeCode:
		claudeHome := getenv("CLAUDE_CONFIG_DIR")
		global := filepath.Join(claudeHome, ".claude.json")
		if claudeHome == "" {
			claudeHome = filepath.Join(home, ".claude")
			global = filepath.Join(home, ".claude.json")
		}
		return "claude", []string{
			filepath.Join(claudeHome, ".credentials.json"),
			filepath.Join(claudeHome, "settings.json"),
			filepath.Join(claudeHome, "plugins", "installed_plugins.json"),
			global,
		}
	case workflow.BackendCodex:
		codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return "codex", []string{
			filepath.Join(codexHome, "auth.json"),
			filepath.Join(codexHome, "models_cache.json"),
			filepath.Join(codexHome, "config.toml"),
		}
	case workflow.BackendPi:
		agentHome := filepath.Join(home, ".pi", "agent")
		return "pi", []string{
			filepath.Join(agentHome, "auth.json"),
			filepath.Join(agentHome, "models.json"),
			filepath.Join(agentHome, "settings.json"),
			filepath.Join(agentHome, "mcp.json"),
		}
	case workflow.BackendOpencode:
		configHome := filepath.Join(home, ".config", "opencode")
		return "opencode", []string{
			filepath.Join(home, ".local", "share", "opencode", "auth.json"),
			filepath.Join(configHome, "config.json"),
			filepath.Join(configHome, "opencode.json"),
			filepath.Join(configHome, "opencode.jsonc"),
		}
	}
	return "", nil
}

// claudeProjectInputs are the project-scope files Claude Code reads from its
// working directory: shared and local settings (which can set model, env such
// as ANTHROPIC_BASE_URL, and tools) and project MCP servers.
func claudeProjectInputs(dir string) []string {
	return []string{
		filepath.Join(dir, ".claude", "settings.json"),
		filepath.Join(dir, ".claude", "settings.local.json"),
		filepath.Join(dir, ".mcp.json"),
	}
}

func findBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range procenv.StandardBinDirs() {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return name // absent: fingerprinted as such
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// fingerprintFile appends one line for path: "absent", or its identity. An
// existing file that cannot be statted or read is an error.
func fingerprintFile(out *bytes.Buffer, kind, path string, hashContent bool, project func(io.Reader) ([]byte, error)) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "%s\x00%s\x00absent\n", kind, path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("harness %s %s: %w", kind, path, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("harness %s %s: not a regular file", kind, path)
	}
	if !hashContent {
		fmt.Fprintf(out, "%s\x00%s\x00%d\x00%d\n", kind, path, st.Size(), st.ModTime().UnixNano())
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("harness %s %s: %w", kind, path, err)
	}
	defer f.Close()
	h := sha256.New()
	if project != nil {
		data, err := project(f)
		if err != nil {
			// Decoder errors can contain untrusted credential bytes. Surface only
			// the input's identity, never its contents or the underlying error.
			return fmt.Errorf("harness %s %s: invalid account/cache state", kind, path)
		}
		h.Write(data)
	} else {
		if _, err := io.Copy(h, f); err != nil {
			return fmt.Errorf("harness %s %s: %w", kind, path, err)
		}
	}
	fmt.Fprintf(out, "%s\x00%s\x00%s\n", kind, path, hex.EncodeToString(h.Sum(nil)))
	return nil
}
