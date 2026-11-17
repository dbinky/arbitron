package config_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileKindChecker is a workflow.ProfileChecker backed by the loaded default
// profile registry. It maps a node's model_profile alias to the registry's
// capability kind so validateModelProfiles enforces the llm_call→chat /
// agent_call→agent contract against the SAME profiles the sample ships with.
type profileKindChecker map[string]string

func (p profileKindChecker) ProfileKind(alias string) (string, bool) {
	k, ok := p[alias]
	return k, ok
}

// allFilesExist is a permissive workflow.FileChecker: the sample references
// copy/skill artifacts by relative path, which are not the subject of this test
// (Task 2 is about the codex backend being a valid agent backend and the
// profile kinds matching). Treating every referenced file as present isolates
// the model-profile/backend validation pass.
type allFilesExist struct{}

func (allFilesExist) FileExists(string, string) bool { return true }

// TestInterviewerProfile_ResolvesToClaudeCodeBinding verifies the full chain
// defaults → Load → ProfilesFromConfig → DynamicProfileResolver → Resolve for the
// "interviewer" alias. The agent factory and stream selector consume the
// ResolvedBinding produced by Resolve, not the raw ProfileConfig struct, so this
// test closes the gap between "the profile is loaded" and "the profile RESOLVES
// to a binding the factory dispatches correctly".
//
// The DEFAULT interviewer is claude-code with the subscription default model
// (model ""). The gpt-5.5/codex mixed-model split ships as a COMMENTED example in
// the sample config (PR #32 Must Fix #4: do not ship a default the authoring host
// could not run). The subscription path (empty ConnectionRef) reveals no key.
func TestInterviewerProfile_ResolvesToClaudeCodeBinding(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver := adapterprovider.NewDynamicProfileResolver(
		adapterprovider.NewProfileCacheFromProfiles(profiles...), nil)

	binding, err := resolver.Resolve(context.Background(), "interviewer")
	require.NoError(t, err, "interviewer alias must resolve (it is present in the default registry)")

	assert.Equal(t, provider.ProfileKindAgent, binding.Kind, "interviewer must be agent-kind")
	// The default interviewer sets no backend, so it resolves to the empty
	// (inherit) backend — floored to the deployment default (claude-code) by the
	// executor before dispatch. The key invariant this pins is that it is NOT the
	// codex backend (which would require codex installed+logged-in and a gpt-5.5
	// plan). The sample's stage-specific admin profiles are tested separately.
	assert.Empty(t, binding.Backend, "default interviewer must inherit the backend (empty), not pin codex")
	assert.NotEqual(t, "codex", binding.Backend, "default interviewer must NOT pin the codex backend")
	for _, p := range profiles {
		if p.Alias == "interviewer" {
			assert.Equal(t, provider.SelectionFollowing, p.SelectionMode, "default interviewer must follow its tier")
			assert.Equal(t, &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}, p.FollowTarget)
		}
	}
	// The subscription path (empty ConnectionRef) never reveals a key: RevealKey
	// must return empty-string, not an error.
	key, keyErr := binding.RevealKey(context.Background())
	require.NoError(t, keyErr, "interviewer RevealKey must not error (subscription-only path)")
	assert.Empty(t, key, "default interviewer must never carry an API key")
}

// TestPlannerCoderProfiles_ResolveToClaudeCodeBindings complements
// TestInterviewerProfile_ResolvesToClaudeCodeBinding by verifying the full
// resolver chain for the planner and coder profiles: both must resolve to
// Backend="claude-code", Kind="agent", Model="opus-4.8" with an empty key
// (subscription-only path). The stream selector dispatches on binding.Backend, so
// an incorrect backend value here would silently route planner/coder to the wrong
// stream adapter.
func TestPlannerCoderProfiles_ResolveToClaudeCodeBindings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver := adapterprovider.NewDynamicProfileResolver(
		adapterprovider.NewProfileCacheFromProfiles(profiles...), nil)

	for _, alias := range []string{"planner", "coder"} {
		alias := alias
		t.Run(alias, func(t *testing.T) {
			binding, err := resolver.Resolve(context.Background(), alias)
			require.NoError(t, err, "%s alias must resolve", alias)
			assert.Equal(t, provider.ProfileKindAgent, binding.Kind, "%s must be agent-kind", alias)
			assert.Equal(t, "claude-code", binding.Backend, "%s must resolve to claude-code backend", alias)
			// Following opus: the concrete model is chosen at seed time from the
			// catalog (TestShippedDefaultProfilesSeedOnFirstBoot), not in config.
			for _, p := range profiles {
				if p.Alias == alias {
					assert.Equal(t, provider.SelectionFollowing, p.SelectionMode, "%s must follow its tier", alias)
					assert.Equal(t, &provider.FollowTarget{Provider: "anthropic", Tier: "opus"}, p.FollowTarget, "%s must follow opus", alias)
				}
			}
			key, keyErr := binding.RevealKey(context.Background())
			require.NoError(t, keyErr, "%s RevealKey must not error (subscription path)", alias)
			assert.Empty(t, key, "%s must not carry an API key (subscription-only)", alias)
		})
	}
}

// The bundled samples run on per-stage admin profiles (effort/context axes) that
// live only in the arbitron DB and cannot be seeded from config.yaml. Each sample
// documents them in its PROFILES.md, the recovery source after a profiles-table
// reseed. So the acceptance is: every sample validates against the default
// profiles PLUS the ones its PROFILES.md documents, with matching kinds. This
// replaces "validates with the default profiles", which has been false since
// the samples moved to per-stage profiles (1cdbe448, #64) and failed on main.
// This test enforces only referenced profile aliases and kinds; it does not
// verify the remaining recovery-table columns or provision runtime profiles.

// documentedProfileRow matches a PROFILES.md table row: | alias | kind | ...
var documentedProfileRow = regexp.MustCompile(`^\|\s*([A-Za-z0-9][A-Za-z0-9-]*)\s*\|\s*(agent|chat)\s*\|`)

// documentedProfiles returns the alias→kind rows from a sample's PROFILES.md.
// A sample without PROFILES.md, or whose PROFILES.md has no `| alias |` table
// (a parent with no model nodes that points at its children's files), documents
// nothing (it must run on the defaults); any other read error fails the test
// rather than masquerading as absence.
func documentedProfiles(t *testing.T, sample string) profileKindChecker {
	t.Helper()
	kinds := profileKindChecker{}
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", sample, "PROFILES.md"))
	if os.IsNotExist(err) {
		return kinds
	}
	require.NoError(t, err, "%s PROFILES.md must be readable", sample)
	hasTable := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| alias |") {
			hasTable = true
		}
		if m := documentedProfileRow.FindStringSubmatch(line); m != nil {
			kinds[m[1]] = m[2]
		}
	}
	if hasTable {
		require.NotEmpty(t, kinds, "%s PROFILES.md has no alias|kind table rows", sample)
	}
	return kinds
}

// bundledSamples discovers every sample that ships a workflow.json, so a new
// sample enters the validation gate without editing this test.
func bundledSamples(t *testing.T) []string {
	t.Helper()
	manifests, err := filepath.Glob(filepath.Join("..", "..", "samples", "*", "workflow.json"))
	require.NoError(t, err)
	require.NotEmpty(t, manifests, "no samples/*/workflow.json found")
	samples := make([]string, 0, len(manifests))
	for _, m := range manifests {
		samples = append(samples, filepath.Base(filepath.Dir(m)))
	}
	return samples
}

func defaultProfileKinds(t *testing.T) profileKindChecker {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := adapterconfig.Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	kinds := profileKindChecker{}
	for _, p := range cfg.Profiles {
		kinds[p.Alias] = p.Kind
	}
	return kinds
}

func loadSampleWorkflow(t *testing.T, sample string) workflow.Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", sample, "workflow.json"))
	require.NoError(t, err, "sample %s workflow must exist", sample)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	return wf
}

// HAPPY / SUCCESS — every bundled sample validates against the defaults plus
// whatever profiles its PROFILES.md (if any) documents.
func TestSampleWorkflows_ValidateWithTheirDocumentedProfiles(t *testing.T) {
	for _, sample := range bundledSamples(t) {
		t.Run(sample, func(t *testing.T) {
			kinds := defaultProfileKinds(t)
			for alias, kind := range documentedProfiles(t, sample) {
				kinds[alias] = kind
			}
			wf := loadSampleWorkflow(t, sample)
			errs := workflow.Validate(&wf, allFilesExist{}, kinds, nil)
			assert.Empty(t, errs, "sample %s must validate with its documented profiles: %+v", sample, errs)
		})
	}
}

// FAILURE — a profile the sample uses but PROFILES.md does not document is
// caught, so the recovery doc cannot silently fall behind the manifest.
func TestSampleWorkflows_UndocumentedProfileFails(t *testing.T) {
	kinds := defaultProfileKinds(t)
	for alias, kind := range documentedProfiles(t, "legacy-feature-pipeline") {
		kinds[alias] = kind
	}
	wf := loadSampleWorkflow(t, "legacy-feature-pipeline")
	var used string
	for _, n := range wf.Nodes {
		if n.Config.ModelProfile != "" {
			used = n.Config.ModelProfile
			break
		}
	}
	require.NotEmpty(t, used, "legacy-feature-pipeline must use at least one model_profile")
	delete(kinds, used)
	errs := workflow.Validate(&wf, allFilesExist{}, kinds, nil)
	require.NotEmpty(t, errs, "dropping %s from the documented profiles must fail validation", used)
}

// EDGE — the table parser reads alias/kind rows and ignores the header,
// separator and non-profile tables (the lane table has no kind column).
func TestDocumentedProfileRow_ParsesOnlyProfileRows(t *testing.T) {
	assert.Nil(t, documentedProfileRow.FindStringSubmatch("| alias | kind | backend |"))
	assert.Nil(t, documentedProfileRow.FindStringSubmatch("|-------|------|---------|"))
	assert.Nil(t, documentedProfileRow.FindStringSubmatch("| single review 1 (wiring lens) | `coder-sonnet5-high` |"))
	m := documentedProfileRow.FindStringSubmatch("| coder-opus-high | agent | claude-code | claude-opus-5-5 | high |")
	require.NotNil(t, m)
	assert.Equal(t, []string{"coder-opus-high", "agent"}, m[1:])
}
