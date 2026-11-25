package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type argvRunner struct {
	mu   sync.Mutex
	args [][]string
}

func (r *argvRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.mu.Lock()
	r.args = append(r.args, spec.Args)
	r.mu.Unlock()
	ch := make(chan string, 1)
	ch <- `{"type":"result","is_error":false,"result":"ok","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-opus-5-5":{}}}`
	close(ch)
	return ch, func() error { return nil }, nil
}

func allowedToolsArg(args []string) []string {
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) {
			return strings.Split(args[i+1], ",")
		}
	}
	return nil
}

func TestAgentPolicy_EffectiveScopeMatchesNativeLaunchArgs(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(work, 0o700))
	r := &argvRunner{}
	f := NewFactory(Deps{ClaudeAgentConfig: claudecli.Config{BinaryPath: "claude", WorkspaceRoot: root, Runner: r, AllowedTools: []string{"Read", "Edit"}, SkipPermissions: true}})
	base := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Provider: ProviderClaudeCLI, Backend: "claude-code", Model: "claude-opus-5-5"}
	withProfile := base
	withProfile.AllowedTools = []string{"Bash"}

	for _, tc := range []struct {
		name      string
		binding   domprovider.ResolvedBinding
		requested []string
		want      []string
	}{
		{"node scope over an empty profile", base, []string{"Grep", "Glob"}, []string{"Grep", "Glob"}},
		{"empty scope resolves the configured global default", base, nil, []string{"Read", "Edit"}},
		{"profile scope wins over the node scope", withProfile, []string{"Grep"}, []string{"Bash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.EffectiveAgentTools(tc.binding, tc.requested)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "effective scope")
			assert.NotEmpty(t, got, "a resolved scope is never an empty universal grant")

			ap, err := f.AgentProviderFor(tc.binding)
			require.NoError(t, err)
			_, err = ap.RunAgent(context.Background(), engine.AgentMission{Model: tc.binding.Model, Prompt: "p", WorkDir: work, ToolScope: got})
			require.NoError(t, err)
			r.mu.Lock()
			launched := allowedToolsArg(r.args[len(r.args)-1])
			r.mu.Unlock()
			assert.Equal(t, got, launched, "the effective scope is exactly what claude is launched with")
		})
	}
}
