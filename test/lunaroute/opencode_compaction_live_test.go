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

// A supporting-call experiment, not the full LR-06 contract/support milestone.
// The operator runs this against the installed binary and real hosted gateway.
func TestLiveOpencodeCompaction(t *testing.T) {
	runLiveOpenCodeExperiment(t, false)
}

func TestLiveOpencodeAgentTask(t *testing.T) {
	runLiveOpenCodeExperiment(t, true)
}

func runLiveOpenCodeExperiment(t *testing.T, completedAgent bool) {
	t.Helper()
	key := liveKey(t)
	model := liveModel(t, "ARBITRON_LUNAROUTE_E2E_OPENCODE_MODEL")
	bin := liveBinary(t, "opencode")
	if versionOf(t, bin) != "1.19.5" {
		t.Fatal("compaction sources require OpenCode 1.19.5; verification pending")
	}
	rules := liveRules(t)
	source, err := readRepoFile(repoRoot(t), evidencePath+"/sources/lr02-live-models-2026-11-11.json")
	if err != nil {
		t.Fatal("recorded model inputs unavailable")
	}
	client := liveClient()
	binding := liveBinding(key, model, "opencode")
	probes := adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: client, Timeout: agentExperimentTimeout, MaxResponseBytes: captureBodyLimit})
	probe, err := probes.ProbeFor(binding)
	if err != nil {
		t.Fatal("cannot construct selected connection probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentExperimentTimeout)
	defer cancel()
	listing, err := probe.Probe(ctx, binding)
	if err != nil || !listing.Reachable || !slices.Contains(listing.Models, model) {
		t.Fatal("selected reviewed model not available; no fallback attempted")
	}
	root, err := os.MkdirTemp("", "lunaroute-compaction-")
	if err != nil {
		t.Fatal("cannot create isolated experiment")
	}
	defer func() {
		if os.RemoveAll(root) != nil {
			t.Error("cannot remove experiment")
		}
	}()
	// Bind snapshots of both inherited config layers and conflicting auth to
	// the real adapter-emitted HOME/cwd, not a reconstructed launch environment.
	home := filepath.Join(root, "home")
	isolation, err := seedOpenCodeIsolation(home, root)
	if err != nil {
		t.Fatal("cannot seed isolated config/auth")
	}
	t.Setenv("HOME", home)
	runID := "opencode-compaction-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	capture := startInferenceCapture(t, runID, "opencode", binding.ConnectionRef, key)
	wrapper := capture.WrapBinary(t, bin)
	pathDir := filepath.Join(root, "bin")
	if os.MkdirAll(pathDir, 0700) != nil || os.Symlink(wrapper, filepath.Join(pathDir, "opencode")) != nil {
		t.Fatal("cannot expose isolated capture wrapper")
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now().UTC()
	journal := &openCodeControlJournal{isolation: isolation}
	factory := liveObservedCompactingFactoryWithClient(root, "", rules, client, capture, source, journal)
	result, workdir := runLiveMission(t, factory, binding, root)
	if completedAgent {
		if finishOpenCodeAgentTask(repoRoot(t), root, workdir, capture, journal, result, started, time.Now().UTC()) != nil {
			t.Fatal("completed OpenCode agent evidence or cleanup failed; readiness remains pending")
		}
		return
	}
	observation, err := collectOpenCodeSupportingEvidence(workdir, capture, journal, result, started, time.Now().UTC())
	if err != nil {
		t.Fatal("current-run controls/workspace/supporting inference incomplete")
	}
	if capture.Close() != nil {
		t.Fatal("capture cleanup failed; no observation written")
	}
	if os.RemoveAll(root) != nil {
		t.Fatal("experiment cleanup failed; no observation written")
	}
	writeObservation(t, key, openCodeSupportingName, observation)
}
