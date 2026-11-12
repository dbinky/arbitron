package lunaroute_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexHelpFixture = `Run Codex non-interactively
Usage: codex exec [OPTIONS] [PROMPT]
Options:
  -c, --config <key=value>
  -m, --model <MODEL>
      --dangerously-bypass-approvals-and-sandbox
  -C, --cd <DIR>
      --json
`
const piHelpFixture = `pi - AI coding assistant
Usage:
  pi [options] [messages...]
Options:
  --model <pattern>
  --mode <mode>
  --print, -p
  --no-session
  --thinking <level>
Environment Variables:
  PI_CODING_AGENT_DIR - Config directory
`

func TestBackendHelpProjection(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		t.Run(backend, func(t *testing.T) {
			version, help := "codex-cli 0.167.1", codexHelpFixture
			if backend == "pi" {
				version, help = "1.5.2", piHelpFixture
			}
			proof, err := parseBackendHelp(backend, []byte(version+"\n"), []byte(help))
			require.NoError(t, err)
			assert.Equal(t, backend, proof.Backend)
			assert.Equal(t, version, proof.Version)
			assert.Contains(t, proof.Options, "--model")
			assert.NotContains(t, fmt.Sprint(proof), "AI coding assistant")
			options := []string{"--config", "--model", "--dangerously-bypass-approvals-and-sandbox", "--cd", "--json"}
			if backend == "pi" {
				options = []string{"--model", "--mode", "--print", "--no-session", "--thinking"}
			}
			assert.ElementsMatch(t, options, proof.Options)
			for _, option := range options {
				got, err := parseBackendHelp(backend, []byte(version), []byte(strings.ReplaceAll(help, option, "--unsupported")))
				require.ErrorIs(t, err, errBackendEvidence, "missing required option %s", option)
				assert.Empty(t, got)
			}
			for name, mutate := range map[string]func() (string, string){
				"empty help":     func() (string, string) { return version, "" },
				"missing option": func() (string, string) { return version, strings.ReplaceAll(help, "--model", "--models") },
				"option only in prose": func() (string, string) {
					return version, strings.ReplaceAll(help, "--model", "--other") + "Example: --model\n"
				},
				"option only in later section": func() (string, string) {
					return version, strings.ReplaceAll(help, "--model", "--other") + "Examples:\n  --model example\n"
				},
				"wrong usage":         func() (string, string) { return version, strings.ReplaceAll(help, "Usage:", "Examples:") },
				"usage only in prose": func() (string, string) { return version, strings.ReplaceAll(help, "Usage:", "Example of Usage:") },
				"empty version":       func() (string, string) { return "", help },
				"secret version":      func() (string, string) { return version + " lr_private-secret", help },
				"multiline version":   func() (string, string) { return version + "\nextra", help },
				"wrong backend version": func() (string, string) {
					if backend == "pi" {
						return "codex-cli 0.167.1", help
					}
					return "1.5.2", help
				},
				"oversized help": func() (string, string) { return version, help + strings.Repeat("x", backendOutputLimit) },
			} {
				t.Run(name, func(t *testing.T) {
					v, h := mutate()
					got, err := parseBackendHelp(backend, []byte(v), []byte(h))
					require.ErrorIs(t, err, errBackendEvidence)
					assert.Empty(t, got)
					assert.NotContains(t, err.Error(), "private-secret")
				})
			}
		})
	}
	_, err := parseBackendHelp("opencode", []byte("1.5.2"), []byte(piHelpFixture))
	require.ErrorIs(t, err, errBackendEvidence)
}

func TestBackendHelpIsolatedProcessSink(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		for _, mode := range []string{"success", "missing model", "exit", "overflow", "cancel"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "LUNAROUTE_API_KEY", "CODEX_HOME", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "HTTPS_PROXY", "NODE_OPTIONS"} {
					t.Setenv(key, "ambient-must-not-reach-help")
				}
				root := t.TempDir()
				bin, err := os.Executable()
				require.NoError(t, err)
				wrapper := filepath.Join(root, "binary")
				marker := filepath.Join(root, "queries")
				script := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/env -u PWD -u SHLVL -u _ %q -test.run=^TestBackendHelpProcessFixture$ -- %q %q %q \"$@\"\n", bin, backend, mode, marker)
				require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
				privateRoot := filepath.Join(root, "private")
				require.NoError(t, os.Mkdir(privateRoot, 0700))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancel" {
					go func() {
						deadline := time.Now().Add(5 * time.Second)
						for time.Now().Before(deadline) {
							if _, err := os.Stat(marker); err == nil {
								cancel()
								return
							}
							time.Sleep(time.Millisecond)
						}
						cancel()
					}()
				}
				proof, err := collectBackendHelp(ctx, backend, wrapper, privateRoot)
				queries, readErr := os.ReadFile(marker)
				require.NoError(t, readErr, "must reach the actual subprocess")
				if mode == "success" {
					require.NoError(t, err)
					assert.Equal(t, "version\nhelp\n", string(queries))
					assert.Equal(t, backend, proof.Backend)
					assert.Contains(t, proof.Options, "--model")
				} else {
					require.ErrorIs(t, err, errBackendEvidence)
					assert.Empty(t, proof)
					assert.NotContains(t, err.Error(), "private-secret")
				}
				entries, err := os.ReadDir(privateRoot)
				require.NoError(t, err)
				assert.Empty(t, entries, "private help environment must be removed on every exit")
			})
		}
	}
}

// The wrapper invokes this subprocess with the exact query argv and isolated
// environment. It refuses missing/foreign inputs rather than ignoring them.
func TestBackendHelpProcessFixture(t *testing.T) {
	idx := slices.Index(os.Args, "--")
	if idx < 0 {
		return
	}
	args := os.Args[idx+1:]
	if len(args) < 4 {
		os.Exit(2)
	}
	backend, mode, marker := args[0], args[1], args[2]
	query := args[3:]
	allowed := []string{"PATH", "HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}
	pointer := "CODEX_HOME"
	if backend == "pi" {
		pointer = "PI_CODING_AGENT_DIR"
		allowed = append(allowed, "PI_OFFLINE")
	}
	allowed = append(allowed, pointer)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(allowed, key) || strings.Contains(kv, "ambient-must-not-reach-help") {
			os.Exit(3)
		}
	}
	cwd, err := os.Getwd()
	if err != nil || filepath.Base(cwd) != "workspace" || os.Getenv("PATH") == "" {
		os.Exit(4)
	}
	parent := filepath.Dir(cwd)
	for _, key := range allowed {
		if key == "PATH" || key == "PI_OFFLINE" {
			continue
		}
		path := os.Getenv(key)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			os.Exit(5)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || filepath.Dir(canonical) != parent {
			os.Exit(6)
		}
	}
	if backend == "pi" && os.Getenv("PI_OFFLINE") != "1" {
		os.Exit(7)
	}
	label, output := "help", codexHelpFixture
	if backend == "pi" {
		output = piHelpFixture
	}
	if slices.Equal(query, []string{"--version"}) {
		label, output = "version", "codex-cli 0.167.1\n"
		if backend == "pi" {
			output = "1.5.2\n"
		}
	} else if backend == "codex" && !slices.Equal(query, []string{"exec", "--help"}) || backend == "pi" && !slices.Equal(query, []string{"--help"}) {
		os.Exit(8)
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(9)
	}
	if _, err := fmt.Fprintln(f, label); err != nil {
		os.Exit(10)
	}
	if f.Close() != nil {
		os.Exit(11)
	}
	if mode == "cancel" {
		time.Sleep(10 * time.Second)
		os.Exit(12)
	}
	if label == "help" {
		switch mode {
		case "missing model":
			output = strings.ReplaceAll(output, "--model", "--models")
		case "exit":
			fmt.Fprintln(os.Stderr, "lr_private-secret Bearer private-secret")
			os.Exit(13)
		case "overflow":
			output += strings.Repeat("x", backendOutputLimit)
		}
	}
	fmt.Print(output)
	os.Exit(0)
}
