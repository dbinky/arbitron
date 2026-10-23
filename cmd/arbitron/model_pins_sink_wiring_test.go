package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelSink is an OpenAI-compatible endpoint that records the "model" of every
// chat-completions body it receives: the final sink the pinned model must reach.
type modelSink struct {
	mu      sync.Mutex
	models  []string
	srv     *httptest.Server
	arrived chan struct{} // receives once per request, after it is recorded
	release chan struct{} // when non-nil, the FIRST request blocks until it is closed
	gated   sync.Once
	content string // assistant reply; defaults to "ok"
}

func newModelSink(t *testing.T) *modelSink {
	t.Helper()
	s := &modelSink{arrived: make(chan struct{}, 16), content: "ok"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		decodeErr := json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.models = append(s.models, body.Model)
		gate, content := s.release, s.content
		s.mu.Unlock()
		s.arrived <- struct{}{}
		if decodeErr != nil || body.Model == "" {
			http.Error(w, "missing execution model", http.StatusBadRequest)
			return
		}
		if gate != nil {
			s.gated.Do(func() { <-gate })
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, body.Model, content)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *modelSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models...)
}

func bootSinkStack(t *testing.T) *domainStack {
	t.Helper()
	return bootSinkRoot(t, "", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-llm", `{
  "name": "pin-llm",
  "description": "one llm call",
  "inputs": [],
  "nodes": [
    {"id": "ask1", "type": "llm_call", "entrypoint": true,
     "config": {"model_profile": "pin-alias", "templateFile": "templates/p.md"}},
    {"id": "ask2", "type": "llm_call",
     "config": {"model_profile": "pin-alias", "templateFile": "templates/p.md"}}
  ],
  "edges": [{"source": "ask1", "target": "ask2"}]
}`)
		writePinTemplate(t, workspaceDir, "pin-llm")
	})
}

// writePinTemplate writes templates/p.md ("hello") under the workflow's dir.
func writePinTemplate(t *testing.T, workspaceDir, workflowName string) {
	t.Helper()
	tplDir := filepath.Join(workspaceDir, "users", localDefaultUser.String(), workflowName, "templates")
	require.NoError(t, os.MkdirAll(tplDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "p.md"), []byte("hello"), 0o600))
}

// bootSinkRoot constructs the REAL initDomain and explicitly prepares its
// configured seed fixtures, without starting serving/recovery/refresh loops.
// Lifecycle integration tests use domainStack.Start instead.
// The workflows are written by setup. llmCfg is extra YAML appended under an `llm:` key
// (empty for none).
func bootSinkRoot(t *testing.T, llmCfg string, setup func(workspaceDir string)) *domainStack {
	ds, _ := bootSinkRootWithClose(t, llmCfg, setup)
	return ds
}

// rootCatalogSource is the catalog leaf of a booted test stack. It has no
// source until a test points it at one with serveRootCatalog, so execution
// fixtures never fetch; a test that exercises the stack's own prospective
// discovery (profile create/edit through the profile service) serves the same
// body as the separate refresh it assembles.
type rootCatalogSource struct {
	mu      sync.Mutex
	fetcher *fixtureFetcher
}

func (s *rootCatalogSource) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	f := s.fetcher
	s.mu.Unlock()
	if f == nil {
		return nil, fmt.Errorf("execution fixture has no catalog source")
	}
	body, err := f.Fetch(r.Context(), r.URL.String())
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
}

var rootCatalogSources sync.Map // *domainStack -> *rootCatalogSource

// serveRootCatalog makes the stack's own candidate source fetch from f.
func serveRootCatalog(t *testing.T, ds *domainStack, f *fixtureFetcher) {
	t.Helper()
	v, ok := rootCatalogSources.Load(ds)
	require.True(t, ok, "stack was not booted by bootSinkRootWithClose")
	source := v.(*rootCatalogSource)
	source.mu.Lock()
	defer source.mu.Unlock()
	source.fetcher = f
}

// The explicit, idempotent close lets restart tests reopen the same database
// through initDomain, while cleanup still closes every stack on a failed test.
func bootSinkRootWithClose(t *testing.T, llmCfg string, setup func(workspaceDir string)) (*domainStack, func()) {
	t.Helper()
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	setup(workspaceDir)
	configPath := filepath.Join(root, "config.yaml")
	// Source fixtures must not accidentally invoke a host's subscribed Claude
	// binary on the installation-wide default route.
	if !strings.Contains(llmCfg, "binarypath:") {
		bin := filepath.Join(root, "unconfigured-claude")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0700))
		if strings.Contains(llmCfg, "llm:\n") {
			llmCfg = strings.Replace(llmCfg, "llm:\n", "llm:\n  binarypath: "+bin+"\n", 1)
		} else {
			llmCfg += "llm:\n  binarypath: " + bin + "\n"
		}
	}
	cfg := "server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" + llmCfg
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	source := &rootCatalogSource{}
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, ModelDependencies{FetchTransport: source})
	require.NoError(t, err)
	rootCatalogSources.Store(ds, source)
	t.Cleanup(func() { rootCatalogSources.Delete(ds) })
	// Tests that assemble a separate refresh supply its own source leaf.
	ds.refreshDeps.FetchTransport = nil
	seedBuiltStack(t, ds)
	var once sync.Once
	closeStack := func() { once.Do(func() { ds.Close(context.Background()) }) }
	t.Cleanup(closeStack)
	return ds, closeStack
}

// Seed-only tests deliberately do not start discovery/recovery after the seed.
func seedBuiltStack(t *testing.T, ds *domainStack) {
	t.Helper()
	_, _, err := SeedProfiles(context.Background(), adaptsqlite.NewProfileRepo(ds.db), ds.profileSvc, configadapter.ProfilesFromConfig(ds.cfg.Profiles), nil)
	require.NoError(t, err)
}

func waitTerminal(t *testing.T, ds *domainStack, id uuid.UUID) *instance.Instance {
	t.Helper()
	var inst *instance.Instance
	require.Eventually(t, func() bool {
		var err error
		inst, err = ds.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.State.IsTerminal()
	}, 20*time.Second, 50*time.Millisecond)
	return inst
}

// TestModelPinsWiringLLMCallSink drives the real initDomain end to end for the
// llm_call consumer: the HTTP request body of a run triggered on X still names X
// after the live profile is edited to Y, while a fresh run names Y. Replacing
// the PinningResolver with the raw live resolver in main.go makes the first run
// send Y and fails the test; a NULL-pin legacy run (pins erased in SQLite)
// deliberately follows the live edit.
func TestModelPinsWiringLLMCallSink(t *testing.T) {
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	ctx := adminCtxForLocalUser(t, ds)

	// The production write path is https-only; the loopback fixture goes in through
	// a repo over the same DB and key with the fixture-only permissive policy.
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	fixtureConns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, fixtureConns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: "deepseek/deepseek-v4-flash"})
	require.NoError(t, err)

	// Gate the first request so the live edit lands while the old run is
	// mid-flight: its SECOND llm_call is resolved after the edit.
	gate := make(chan struct{})
	sink.mu.Lock()
	sink.release = gate
	sink.mu.Unlock()

	id, _, err := ds.eng.Trigger(ctx, "pin-llm", map[string]any{}, "normal")
	require.NoError(t, err)
	select {
	case <-sink.arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("first llm_call never reached the sink")
	}
	_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: "deepseek/deepseek-v4.1-flash"})
	require.NoError(t, err)
	close(gate)

	inst := waitTerminal(t, ds, id)
	if inst.State != instance.StateCompleted {
		var nerr string
		_ = ds.db.QueryRow(`SELECT COALESCE(error,'') FROM node_executions WHERE instance_id = ? AND error IS NOT NULL`, id.String()).Scan(&nerr)
		t.Fatalf("run ended %s: node error: %s", inst.State, nerr)
	}
	assert.Equal(t, []string{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash"}, sink.seen(),
		"both calls of the old run must send the pinned model despite the live edit")

	// A fresh run after the edit pins and sends Y.
	sink.mu.Lock()
	sink.release = nil
	sink.mu.Unlock()
	id2, _, err := ds.eng.Trigger(ctx, "pin-llm", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id2).State)
	assert.Equal(t, []string{"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash"}, sink.seen()[2:])

	// Emulate a pre-migration row, not a new trigger with missing wiring: legacy
	// runs alone keep live resolution and their second call follows the edit.
	id3 := uuid.New()
	_, err = ds.db.Exec(`INSERT INTO instances (
  id, owner_id, workflow_name, workflow_snapshot, state, priority,
  variables, original_variables, workflow_id, visibility, model_pins, model_pins_version
) SELECT ?, owner_id, workflow_name, workflow_snapshot, 'queued', priority,
  variables, original_variables, workflow_id, visibility, NULL, 0
  FROM instances WHERE id = ?`, id3.String(), id2.String())
	require.NoError(t, err)
	gate2 := make(chan struct{})
	sink.mu.Lock()
	sink.release = gate2
	sink.gated = sync.Once{}
	sink.mu.Unlock()
	for len(sink.arrived) > 0 { // drop arrivals left by earlier runs
		<-sink.arrived
	}
	before := len(sink.seen())
	_, err = ds.queueMgr.Enqueue(ctx, id3, localDefaultUser)
	require.NoError(t, err)
	legacy, err := ds.instanceRepo.Get(ctx, id3)
	require.NoError(t, err)
	require.Equal(t, instance.ModelPinsVersionLegacy, legacy.ModelPinsVersion)
	select {
	case <-sink.arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("legacy run's first llm_call never reached the sink")
	}
	_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: "deepseek/deepseek-v4-flash:0731"})
	require.NoError(t, err)
	close(gate2)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id3).State)
	assert.Equal(t, []string{"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4-flash:0731"}, sink.seen()[before:],
		"a legacy run follows live edits")
}
