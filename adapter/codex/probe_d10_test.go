package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const d10IdentityPrompt = "Reply with only your model identity on one line. Do not use tools or take any other action."

// This leaf refuses arbitrary -c strings: it checks the source-verified recipe,
// the actual account files and every launch input before writing rollout proof.
type d10Runner struct {
	t                          *testing.T
	home, workdir, form, fault string
	listed                     bool
	calls                      int
}

func (r *d10Runner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	if slices.Equal(spec.Args, []string{"--version"}) {
		version := "codex-cli 0.163.0"
		if r.fault == "unsupported version" {
			version = "codex-cli 0.162.0"
		}
		return scriptLines(version), func() error { return nil }, nil
	}
	r.calls++
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	// Installed 0.163.0 refuses `--strict-config` for `codex mcp`; turns keep it.
	if slices.Contains(spec.Args, "mcp") == slices.Contains(spec.Args, "--strict-config") || spec.Bin != "codex" || spec.Dir != r.workdir || !slices.Contains(spec.Env, "CODEX_HOME="+r.home) || slices.ContainsFunc(spec.Env, func(s string) bool { return strings.HasPrefix(s, "OPENAI_API_KEY=") }) {
		return nil, nil, fmt.Errorf("missing account/binary/workdir")
	}
	auth, err := os.ReadFile(filepath.Join(r.home, "auth.json"))
	if err != nil || string(auth) != "subscription-auth" {
		return nil, nil, fmt.Errorf("account unreadable")
	}
	configs := map[string]string{}
	for i, a := range spec.Args {
		if a == "-c" && i+1 < len(spec.Args) {
			k, v, ok := strings.Cut(spec.Args[i+1], "=")
			if !ok {
				return nil, nil, fmt.Errorf("invalid config")
			}
			configs[k] = v
		}
	}
	for k, v := range map[string]string{"features.plugins": "false", "features.apps": "false", "web_search": `"disabled"`, "sandbox_workspace_write.network_access": "false"} {
		if configs[k] != v {
			return nil, nil, fmt.Errorf("missing restriction %s", k)
		}
	}
	if slices.Contains(spec.Args, "mcp") {
		if !slices.Contains(spec.Args, "list") || !slices.Contains(spec.Args, "--json") || spec.Stdin != "" {
			return nil, nil, fmt.Errorf("wrong config enumeration")
		}
		raw, err := os.ReadFile(filepath.Join(r.home, "config.toml"))
		if err != nil || !strings.Contains(string(raw), "[mcp_servers.hostile]") || !strings.Contains(string(raw), "enabled = true") {
			return nil, nil, fmt.Errorf("missing inherited config")
		}
		r.listed = true
		if r.fault == "unreadable config" {
			return nil, nil, assert.AnError
		}
		if r.fault == "malformed listing" {
			return scriptLines(`{"name":"hostile"}`), func() error { return nil }, nil
		}
		if r.fault == "oversized listing" {
			return scriptLines(strings.Repeat(" ", 1<<20) + `[]`), func() error { return nil }, nil
		}
		if r.fault == "duplicate listing" {
			return scriptLines(`[{"name":"hostile"},{"name":"hostile"}]`), func() error { return nil }, nil
		}
		return scriptLines(`[{"name":"hostile","enabled":true}]`), func() error { return nil }, nil
	}
	actualModel, _ := argValue(spec.Args, "-m")
	if actualModel != "gpt-5-sol" {
		return nil, nil, fmt.Errorf("missing exact model flag")
	}
	if !r.listed || configs["mcp_servers"] != `{"hostile"={enabled=false}}` || spec.Stdin != d10IdentityPrompt || !slices.Contains(spec.Args, "gpt-5-sol") || !slices.Contains(spec.Args, "--json") || slices.Contains(spec.Args, bypassFlag) {
		return nil, nil, fmt.Errorf("missing verified restrictions/model/prompt")
	}
	if r.form == "resume" {
		if !slices.Contains(spec.Args, "resume") || !slices.Contains(spec.Args, rolloutThread) || configs["sandbox_mode"] != `"read-only"` || slices.Contains(spec.Args, "-s") || slices.Contains(spec.Args, "--cd") || spec.Args[len(spec.Args)-1] != "-" {
			return nil, nil, fmt.Errorf("wrong resume posture")
		}
	} else {
		flag := "-s"
		if r.form == "batch" {
			flag = "--sandbox"
		}
		sandbox, _ := argValue(spec.Args, flag)
		cwd, _ := argValue(spec.Args, "--cd")
		if sandbox != "read-only" || cwd != r.workdir {
			return nil, nil, fmt.Errorf("wrong initial posture")
		}
	}
	if r.fault == "unsupported restrictions" || r.fault == "spawn" {
		return nil, nil, fmt.Errorf("subscription-auth refused")
	}
	return scriptLines(execLines(rolloutThread, "identity", 1, 1)...), func() error {
		if r.fault == "exit" {
			return fmt.Errorf("subscription-auth process failure")
		}
		actual := "gpt-5-sol"
		if r.fault == "wrong model" {
			actual = "fallback"
		}
		if r.fault == "missing rollout" {
			return nil
		}
		lines := []string{metaLine(rolloutThread), ctxLine(actual)}
		if r.form == "resume" {
			lines = []string{metaLine(rolloutThread), ctxLine("gpt-5-sol"), ctxLine(actual)}
		}
		if r.fault == "wrong turn" && r.form == "resume" {
			lines = lines[:2]
		}
		if r.fault == "wrong session" {
			lines[0] = metaLine(rolloutThread2)
		}
		writeRollout(r.t, r.home, rolloutThread, lines...)
		return nil
	}, nil
}
func scriptLines(lines ...string) <-chan string {
	out := make(chan string, len(lines))
	for _, line := range lines {
		out <- line
	}
	close(out)
	return out
}

func TestCodexProbeD10Restrictions(t *testing.T) {
	for _, form := range []string{"batch", "initial", "resume"} {
		for _, fault := range []string{"exact", "wrong cache", "wrong model", "wrong turn", "wrong prior", "wrong session", "missing rollout", "malformed listing", "oversized listing", "duplicate listing", "unreadable config", "unreadable account", "unsupported restrictions", "unsupported version", "spawn", "exit", "cancel"} {
			if (fault == "wrong turn" || fault == "wrong prior") && form != "resume" {
				continue
			}
			t.Run(form+"/"+fault, func(t *testing.T) {
				home, wd := t.TempDir(), t.TempDir()
				t.Setenv("CODEX_HOME", home)
				t.Setenv("OPENAI_API_KEY", "unrelated-key")
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte("subscription-auth"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.hostile]\ncommand = 'never-run'\n[plugins.\"hostile@local\"]\nenabled = true\n"), 0600))
				cache := "gpt-5-sol"
				if fault == "wrong cache" {
					cache = "other"
				}
				writeModelsCache(t, home, cache)
				if fault == "unreadable account" {
					require.NoError(t, os.Remove(filepath.Join(home, "auth.json")))
				}
				r := &d10Runner{t: t, home: home, workdir: wd, form: form, fault: fault}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if fault == "cancel" {
					cancel()
				}
				b := codexBinding("gpt-5-sol", nil)
				var got string
				var err error
				if form == "batch" {
					got, _, err = New(Config{Binding: b, BinaryPath: "codex", Runner: r}).ProbeExecutedModel(ctx, "ignored caller prompt", wd)
				} else {
					req := engine.ChatRequest{Model: b.Model, Prompt: "ignored caller prompt", WorkDir: wd, SuppressTools: true, RequireExecutedModel: true}
					if form == "resume" {
						prior := "gpt-5-sol"
						if fault == "wrong prior" {
							prior = "fallback"
						}
						writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine(prior))
						req.SessionID = rolloutThread
					}
					var resp *engine.ChatResponse
					resp, err = NewInteractiveProvider(InteractiveConfig{Binding: b, BinaryPath: "codex", Runner: r}).Chat(ctx, req)
					if resp != nil {
						got = resp.Model
					}
				}
				if fault == "exact" {
					require.NoError(t, err)
					assert.Equal(t, "gpt-5-sol", got)
					assert.True(t, r.listed)
				} else {
					require.Error(t, err)
					assert.Empty(t, got)
					assert.NotContains(t, err.Error(), "subscription-auth")
					assert.NotContains(t, err.Error(), "unrelated-key")
				}
				if fault == "wrong cache" || fault == "cancel" || fault == "unsupported version" || fault == "wrong prior" {
					assert.Zero(t, r.calls)
				}
			})
		}
	}
}
