// Package probe holds the runner health probers: read-only auth/capacity
// probes (no tokens, no slot) and the slot-counted confirmation prober.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/runner"
)

const probeTimeout = 10 * time.Second

// ClaudeAuthProber runs `claude auth status` with ANTHROPIC_API_KEY removed
// (a stray key masks the subscription login).
type ClaudeAuthProber struct {
	bin    string
	runner claudecli.CommandRunner
}

// NewClaudeAuthProber resolves the binary like the adapters do; nil r = os/exec.
func NewClaudeAuthProber(binary string, r claudecli.CommandRunner) *ClaudeAuthProber {
	if r == nil {
		r = claudecli.NewExecRunner()
	}
	bin, _ := claudecli.ResolveBinary(binary)
	return &ClaudeAuthProber{bin: bin, runner: r}
}

// Probe implements the observed contract (docs/evidence/remote-runners/claude-auth-status.md):
// loggedIn:true + exit 0 = ok; loggedIn:false + non-zero exit = logged out;
// anything else = unknown (never parks, never recovers).
func (p *ClaudeAuthProber) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	if s.Kind != runner.CredAnthropicSubscription {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	lines, wait, err := p.runner.Run(ctx, claudecli.RunSpec{
		Bin: p.bin, Args: []string{"auth", "status"}, Env: withoutVar(procenv.BaseEnv(), "ANTHROPIC_API_KEY"), MaxResponseBytes: 64 << 10,
	})
	if err != nil {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	werr := wait()
	if ctx.Err() != nil || errors.Is(werr, context.Canceled) || errors.Is(werr, context.DeadlineExceeded) {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	loggedIn, ok := parseLoggedIn(strings.Join(out, "\n"))
	switch {
	case ok && loggedIn && werr == nil:
		return runner.ProbeResult{Auth: runner.AuthOK}, nil
	case ok && !loggedIn && werr != nil:
		return runner.ProbeResult{Auth: runner.AuthLoggedOut}, nil
	default:
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
}

// parseLoggedIn reads only the boolean `loggedIn` key of a JSON object; other
// keys (email, org, plan) are never read, stored or logged.
func parseLoggedIn(s string) (loggedIn bool, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &m) != nil {
		return false, false
	}
	raw, present := m["loggedIn"]
	if !present || (string(raw) != "true" && string(raw) != "false") || json.Unmarshal(raw, &loggedIn) != nil {
		return false, false
	}
	return loggedIn, true
}

func withoutVar(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}
