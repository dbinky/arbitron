package lunaroute_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
)

// A narrow operator experiment, not TestLiveClaudeCodeAgentTask: no helper,
// memory-loading or complete LR-05 verification is claimed. Main context and
// actual request output budgets are collected without inferring gateway maxima.
func TestLiveClaudeCodeIsolation(t *testing.T) {
	key := liveKey(t)
	model := liveModel(t, "ARBITRON_LUNAROUTE_E2E_CLAUDE_MODEL")
	bin := liveBinary(t, "claude")
	rules := liveRules(t)
	for _, source := range []string{claudeIsolationSource, claudeLimitsSource} {
		if _, err := readRepoFile(repoRoot(t), source); err != nil {
			t.Fatal("isolation/control source unavailable")
		}
	}
	binding := liveBinding(key, model, "claude-code")
	client := liveClient()
	probes := adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: client, Timeout: agentExperimentTimeout, MaxResponseBytes: captureBodyLimit})
	probe, err := probes.ProbeFor(binding)
	if err != nil {
		t.Fatal("cannot construct selected connection probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentExperimentTimeout)
	defer cancel()
	listing, err := probe.Probe(ctx, binding)
	if err != nil || !listing.Reachable || !slices.Contains(listing.Models, model) {
		t.Fatal("selected reviewed model unavailable; no fallback attempted")
	}
	root, err := os.MkdirTemp("", "lunaroute-claude-isolation-")
	if err != nil {
		t.Fatal("cannot create private experiment")
	}
	defer func() {
		if os.RemoveAll(root) != nil {
			t.Error("cannot remove private experiment")
		}
	}()
	ws := filepath.Join(root, "mission")
	if os.Mkdir(ws, 0700) != nil {
		t.Fatal("cannot create private workspace")
	}
	isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
	if err != nil {
		t.Fatal("cannot seed private config/auth")
	}
	t.Setenv("HOME", isolation.home)
	// Remove the operator's relocation before production environment emission.
	// The collector refuses any relocation in the actual emitted launch.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if os.Unsetenv("CLAUDE_CONFIG_DIR") != nil {
		t.Fatal("cannot isolate config location")
	}
	capture := startInferenceCapture(t, "claude-isolation-"+time.Now().UTC().Format("20060102T150405.000000000Z"), "claude-code", binding.ConnectionRef, key)
	wrapper := capture.WrapBinary(t, bin)
	factory := liveClaudeIsolationFactoryWithClient(root, wrapper, rules, client, isolation)
	started := time.Now().UTC()
	result, err := executePreparedAgentMission(ctx, factory, binding, ws)
	if err != nil {
		t.Fatal("isolated Claude mission failed; release acceptance remains pending")
	}
	observation, err := collectClaudeIsolationEvidence(isolation, capture.Records(), result, started, time.Now().UTC())
	if err != nil {
		t.Fatal("current-run config/auth/workspace/inference proof incomplete")
	}
	if capture.Close() != nil || os.RemoveAll(root) != nil {
		t.Fatal("experiment cleanup failed; no observation written")
	}
	writeObservation(t, key, "lr05-isolation.json", observation)
}
