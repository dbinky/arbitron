package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLayoutBuild_MakeAllRequiresFreshAssetsAndBrowser(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is required for the build-gate contract test")
	}
	// --no-print-directory: under CI these tests run inside make, and GNU make
	// then wraps a sub-make's output in Entering/Leaving directory lines.
	cmd := exec.Command("make", "--no-print-directory", "-n", "test-all")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	text := string(out)
	// npm ci is a stamp-file prerequisite, so a dry run only shows it when
	// package-lock.json changed; the build itself always runs.
	ui := strings.Index(text, "cd dashboard && npm run build")
	goTests := strings.Index(text, "go test -timeout 30m ./...")
	render := strings.Index(text, "ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ ./cmd/arbitron/")
	require.GreaterOrEqual(t, ui, 0, "gate must rebuild embedded render.html")
	require.Greater(t, goTests, ui, "Go tests must not see stale assets")
	require.Greater(t, render, ui, "browser-required adapter/root gate must run")
	fresh := strings.Index(text, "git status --porcelain --untracked-files=all -- adapter/dashboard/dist")
	require.Greater(t, fresh, ui, "the gate must prove the committed dist matches the build, untracked files included")
	require.Greater(t, goTests, fresh, "Go tests run only after the dist freshness check")

	cmd = exec.Command("make", "--no-print-directory", "-n", "test")
	cmd.Dir = filepath.Join("..", "..")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	// cmd/arbitron alone runs ~7 minutes; beside the other packages it outlives
	// go test's 10-minute default, so the targets set their own bound.
	assert.Equal(t, "go test -timeout 30m ./...", strings.TrimSpace(string(out)), "make test is the Go-only inner loop")
}

func TestLayoutBuild_DockerShipsPinnedBrowser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	require.NoError(t, err)
	// Check the final runtime stage, not a disposable build-stage installation.
	stages := strings.Split(string(raw), "\nFROM ")
	runtime := stages[len(stages)-1]
	for _, pkg := range []string{
		"chromium=131.0.6778.108-r0",
		"font-noto=23.7.1-r0",
		"font-noto-cjk=0_git20220127-r1",
		"font-noto-emoji=2.042-r0",
	} {
		assert.Contains(t, runtime, "        "+pkg+" \\\n", "runtime apk installation must ship %s", pkg)
	}
}

func TestLayoutBuild_DockerEvidenceEnvironment(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ENV ARBITRON_LAYOUT_BROWSERNOSANDBOX=true \\\n    ARBITRON_LAYOUT_ARTIFACTDIR=/data/layout-artifacts")
}

func TestLayoutBuild_CIRequiresTheGate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "layout.yml"))
	require.NoError(t, err)
	type step struct {
		Name string            `yaml:"name"`
		ID   string            `yaml:"id"`
		If   string            `yaml:"if"`
		Uses string            `yaml:"uses"`
		Run  string            `yaml:"run"`
		With map[string]any    `yaml:"with"`
		Env  map[string]string `yaml:"env"`
	}
	var wf struct {
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress bool   `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Jobs map[string]struct {
			TimeoutMinutes int    `yaml:"timeout-minutes"`
			Steps          []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf))

	// One run per change: pushes only on main, every pull request unfiltered
	// (the required gate never path-filters), superseded runs cancelled.
	push, ok := wf.On["push"].(map[string]any)
	require.True(t, ok, "push must be restricted to main")
	assert.Equal(t, []any{"main"}, push["branches"])
	assert.NotContains(t, push, "paths")
	pr, ok := wf.On["pull_request"]
	require.True(t, ok, "pull_request is required")
	assert.Nil(t, pr, "do not path/branch-filter pull requests")
	assert.NotContains(t, wf.On, "pull_request_target", "PR code never runs with base-repo rights")
	assert.Equal(t, map[string]string{"contents": "read"}, wf.Permissions)
	assert.Contains(t, wf.Concurrency.Group, "github.ref")
	assert.True(t, wf.Concurrency.CancelInProgress)

	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+@[0-9a-f]{40}$`)
	var goSetup, nodeSetup, browser, npm, gate, fontGated, fontRun, fontCached bool
	for name, job := range wf.Jobs {
		assert.Positive(t, job.TimeoutMinutes, "job %s must bound its runtime", name)
		for _, st := range job.Steps {
			if st.Uses != "" {
				assert.Regexp(t, pinned, st.Uses, "pin %q to a full commit SHA", st.Uses)
			}
			switch {
			case strings.HasPrefix(st.Uses, "actions/setup-go@"):
				goSetup = st.With["go-version-file"] == "go.mod"
			case strings.HasPrefix(st.Uses, "actions/setup-node@"):
				nodeSetup = st.With["node-version"] == "22"
			case strings.HasPrefix(st.Uses, "browser-actions/setup-chrome@"):
				browser = true
			case strings.HasPrefix(st.Uses, "docker/build-push-action@"):
				fontCached = st.With["cache-from"] == "type=gha" && st.With["tags"] == "arbitron:layout-render" && st.With["load"] == true
			}
			if strings.Contains(st.Run, "npm ci") {
				npm = true
			}
			if st.Run == "make test-gate" {
				gate = strings.Contains(st.Env["ARBITRON_LAYOUT_BROWSERPATH"], "chrome-path")
			}
			if strings.HasPrefix(st.Run, "make test-font-portability") {
				fontRun = true
				fontGated = strings.Contains(st.If, "fonts.outputs.changed")
			}
		}
	}
	assert.True(t, goSetup)
	assert.True(t, nodeSetup)
	assert.True(t, browser)
	assert.True(t, npm)
	assert.True(t, gate, "CI must feed the installed browser executable to make test-gate")
	assert.True(t, fontRun, "CI must run the font-portability proof")
	assert.True(t, fontGated, "font portability runs when its inputs change (always on main)")
	assert.True(t, fontCached, "the image build behind font portability is layer-cached")
	for _, input := range []string{"adapter/renderreview/", "dashboard/src/graph/arranged/", "dashboard/scripts/", "Dockerfile", "dashboard/package-lock.json"} {
		assert.Contains(t, string(raw), input, "font portability must re-run when %s changes", input)
	}
	assert.NotContains(t, string(raw), "continue-on-error")
	assert.NotContains(t, string(raw), "ARBITRON_REQUIRE_BROWSER: '0'")
	assert.NotContains(t, string(raw), "BROWSERNOSANDBOX", "CI exercises the sandboxed browser, never the no-sandbox fallback")
	assert.Contains(t, string(raw), "kernel.apparmor_restrict_unprivileged_userns=0", "runners must allow Chrome's user-namespace sandbox")
}

// Locally, test-all stays the whole gate: CI's two jobs together run exactly it.
func TestLayoutBuild_TestAllIsGatePlusFontPortability(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`(?m)^test-all: test-gate test-font-portability$`), string(raw))
	assert.Regexp(t, regexp.MustCompile(`(?m)^test-gate: check-ui-fresh test-go-fresh-ui test-ui test-render test-dashboard-browser$`), string(raw))
}

// Plain `go test ./...` skips every real-browser test (see browser_gate_test.go),
// so the browser gate is their ONLY run. It must take both packages whole: a
// -run name filter silently dropped 18 browser tests (e.g.
// TestBrowserRenderer_RelaunchesAfterBrowserDies) that only ever ran by accident
// under plain `go test` while Chrome happened to be installed.
func TestLayoutBuild_RenderGateRunsEveryBrowserTest(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is required for the build-gate contract test")
	}
	cmd := exec.Command("make", "--no-print-directory", "-n", "test-render")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var gate string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ARBITRON_REQUIRE_BROWSER=1 go test") {
			gate = line
		}
	}
	require.NotEmpty(t, gate, "test-render must run go test with the browser gate on")
	assert.Contains(t, gate, "./adapter/renderreview/")
	assert.Contains(t, gate, "./cmd/arbitron/")
	assert.NotContains(t, gate, "-run", "a name filter drops browser tests from their only run")
}

// The marker-namespace and font-portability proofs are browser-only dashboard
// scripts (dashboard/package.json). Without a Make target in test-all they
// regress silently (gap G1).
func TestLayoutBuild_TestAllRunsDashboardBrowserScripts(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is required for the build-gate contract test")
	}
	cmd := exec.Command("make", "--no-print-directory", "-n", "test-all")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	text := string(out)
	arranged := strings.Index(text, "ARBITRON_REQUIRE_BROWSER=1 npm run test:arranged-browser")
	require.GreaterOrEqual(t, arranged, 0, "test-all must run the arranged markers/fonts browser scripts with the browser required")
	build := strings.Index(text, "docker build -t arbitron:layout-render")
	portability := strings.Index(text, "npm run test:font-portability")
	require.GreaterOrEqual(t, build, 0, "font portability runs in the shipped image, which the gate must build first")
	require.Greater(t, portability, build, "font portability must run after the image it exercises is built")

	pkg, err := os.ReadFile(filepath.Join("..", "..", "dashboard", "package.json"))
	require.NoError(t, err)
	assert.Contains(t, string(pkg), "scripts/test-arranged-markers-browser.mjs", "test:arranged-browser must include the markers script")
}
