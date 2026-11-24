// adapter/process/executor_bundle_test.go
package process_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRunner records every ProcessSpec it receives and delegates to the
// real local runner so the step still produces real output.
type recordingRunner struct {
	mu    sync.Mutex
	specs []engine.ProcessSpec
	inner engine.ProcessRunner
}

func (r *recordingRunner) RunProcess(ctx context.Context, spec engine.ProcessSpec) (engine.ProcessResult, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.inner.RunProcess(ctx, spec)
}

func (r *recordingRunner) last(t *testing.T) engine.ProcessSpec {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.specs)
	return r.specs[len(r.specs)-1]
}

func libFixture(t *testing.T) (base string) {
	t.Helper()
	base = t.TempDir()
	libTools := filepath.Join(base, "shared", workflow.LibraryFolder, "tools")
	writeFile(t, filepath.Join(libTools, "run.sh"), "#!/bin/sh\n. ./common.sh\n", 0o755)
	writeFile(t, filepath.Join(libTools, "common.sh"), "printf '{\"lib\":true,\"arg\":\"%s\"}' \"$1\"\n", 0o644)
	return base
}

func TestScriptExecutor_StampsBundleAndRelativeEntrypoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	ctx := context.Background()
	base := libFixture(t)
	store, err := checkpoint.NewBundleStore(filepath.Join(t.TempDir(), "bundles"))
	require.NoError(t, err)
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewScriptExecutor(filesystem.NewFileReader(base), nil, rec)
	exec.SetBundleStore(store)

	node := workflow.Node{ID: "lib", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{
		ScriptFile: "@lib/tools/run.sh", Interpreter: "/bin/sh", Args: []string{"{{who}}"}, Portable: true, // RR-09: only portable scripts are bundled
	}}
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("who", "world"))
	res, err := exec.Execute(ctx, node, vars, "wf") // the shared _lib resolves for any owner
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"lib": true, "arg": "world"}, res.Output, "the built-in run still works from the script folder")

	spec := rec.last(t)
	require.NotNil(t, spec.Bundle, "an executor with a bundle store must stamp the bundle")
	assert.Equal(t, "run.sh", spec.Bundle.Entrypoint)
	assert.Equal(t, []string{"./run.sh", "world"}, spec.Args, "never the absolute server script path")
	has, err := store.Has(ctx, spec.Bundle.Hash)
	require.NoError(t, err)
	assert.True(t, has, "the bundle is stored under its hash before dispatch")

	rc, err := store.Open(ctx, spec.Bundle.Hash)
	require.NoError(t, err)
	dst := t.TempDir()
	require.NoError(t, process.ExtractScriptBundle(rc, dst))
	require.NoError(t, rc.Close())
	_, err = os.Stat(filepath.Join(dst, "common.sh"))
	require.NoError(t, err, "the stored bundle carries the lib sibling")
}

func TestScriptExecutor_UnflaggedScriptRunsUnbundled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho '{\"ok\":true}'\n", 0o755)
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(dir, "outside")))
	fr := &mapFileReader{paths: map[string]string{"wf/run.sh": filepath.Join(dir, "run.sh")}}
	store, err := checkpoint.NewBundleStore(filepath.Join(t.TempDir(), "bundles"))
	require.NoError(t, err)
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewScriptExecutor(fr, nil, rec)
	exec.SetBundleStore(store)

	node := workflow.Node{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{ScriptFile: "run.sh", Interpreter: "/bin/sh"}}
	res, err := exec.Execute(ctx, node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ok": true}, res.Output)
	spec := rec.last(t)
	assert.Nil(t, spec.Bundle, "RR-09 decision 2026-11-22: an unflagged script is never bundled (built-in only)")
	assert.False(t, spec.Portable)
	assert.Equal(t, filepath.Join(dir, "run.sh"), spec.Args[0])
}

func TestScriptExecutor_PortableScriptThatBecameUnbundleableFailsVisibly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho '{\"ok\":true}'\n", 0o755)
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(dir, "outside"))) // added after load-time validation
	fr := &mapFileReader{paths: map[string]string{"wf/run.sh": filepath.Join(dir, "run.sh")}}
	store, err := checkpoint.NewBundleStore(filepath.Join(t.TempDir(), "bundles"))
	require.NoError(t, err)
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewScriptExecutor(fr, nil, rec)
	exec.SetBundleStore(store)

	node := workflow.Node{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{ScriptFile: "run.sh", Interpreter: "/bin/sh", Portable: true, Requires: []string{"jq"}}}
	_, err = exec.Execute(ctx, node, variable.NewStore(), "wf")
	require.ErrorIs(t, err, process.ErrBundleOutsideReference, "never a silent built-in downgrade")
	assert.Empty(t, rec.specs, "no process started")
}

func TestScriptExecutor_PortableStampsRequires(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho '{\"ok\":true}'\n", 0o755)
	fr := &mapFileReader{paths: map[string]string{"wf/run.sh": filepath.Join(dir, "run.sh")}}
	store, err := checkpoint.NewBundleStore(filepath.Join(t.TempDir(), "bundles"))
	require.NoError(t, err)
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewScriptExecutor(fr, nil, rec)
	exec.SetBundleStore(store)

	requires := []string{"jq", "git"}
	node := workflow.Node{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{ScriptFile: "run.sh", Interpreter: "/bin/sh", Portable: true, Requires: requires}}
	_, err = exec.Execute(ctx, node, variable.NewStore(), "wf")
	require.NoError(t, err)
	spec := rec.last(t)
	require.NotNil(t, spec.Bundle)
	assert.True(t, spec.Portable)
	assert.Equal(t, []string{"jq", "git"}, spec.Requires)
	requires[0] = "mutated"
	assert.Equal(t, "jq", spec.Requires[0], "defensive copy")
}

func TestToolCallExecutor_StampsPortableAndRequires(t *testing.T) {
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewToolCallExecutor(nil, rec)
	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, t.TempDir())
	node := workflow.Node{ID: "t", Type: workflow.NodeTypeToolCall, Config: workflow.NodeConfig{Command: "true", Portable: true, Requires: []string{"git"}}}
	_, _ = exec.Execute(context.Background(), node, vars, "wf")
	spec := rec.last(t)
	assert.True(t, spec.Portable)
	assert.Equal(t, []string{"git"}, spec.Requires)
}

func TestScriptExecutor_NoBundleStoreKeepsPhase1Spec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho '{}'\n", 0o755)
	fr := &mapFileReader{paths: map[string]string{"wf/run.sh": filepath.Join(dir, "run.sh")}}
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewScriptExecutor(fr, nil, rec)
	node := workflow.Node{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{ScriptFile: "run.sh", Interpreter: "/bin/sh"}}
	_, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Nil(t, rec.last(t).Bundle)
}

// Workspace-relative tool commands run with the workspace as cwd (design §8,
// intentional fix: Phase 1 left commandSpec.Dir unset).
func TestToolCallExecutor_RunsInWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell commands")
	}
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "tool.sh"), "#!/bin/sh\nprintf '{\"cwd\":\"%s\"}' \"$(pwd -P)\"\n", 0o755)
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewToolCallExecutor(nil, rec)
	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, ws)

	node := workflow.Node{ID: "t", Type: workflow.NodeTypeToolCall, Config: workflow.NodeConfig{Command: "./tool.sh"}}
	res, err := exec.Execute(context.Background(), node, vars, "wf")
	require.NoError(t, err)
	realWS, err := filepath.EvalSymlinks(ws)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"cwd": realWS}, res.Output)
	spec := rec.last(t)
	assert.Equal(t, ws, spec.Dir)
	assert.Equal(t, ws, spec.WorkDir)
}

func TestToolCallExecutor_NoWorkspaceLeavesDirUnset(t *testing.T) {
	rec := &recordingRunner{inner: process.NewLocalProcessRunner()}
	exec := process.NewToolCallExecutor(nil, rec)
	node := workflow.Node{ID: "t", Type: workflow.NodeTypeToolCall, Config: workflow.NodeConfig{Command: "echo", Args: []string{"{}"}}}
	_, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, "", rec.last(t).Dir)
}

// mapFileReader is a minimal engine.FileReader keyed "<workflow>/<relative>".
type mapFileReader struct{ paths map[string]string }

func (m *mapFileReader) ReadFile(_ uuid.UUID, wf, rel string) ([]byte, error) {
	p, ok := m.paths[wf+"/"+rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(p)
}
func (m *mapFileReader) ResolvePath(_ uuid.UUID, wf, rel string) (string, error) {
	p, ok := m.paths[wf+"/"+rel]
	if !ok {
		return "", os.ErrNotExist
	}
	return p, nil
}
func (m *mapFileReader) FileExists(_ uuid.UUID, wf, rel string) bool {
	_, ok := m.paths[wf+"/"+rel]
	return ok
}
