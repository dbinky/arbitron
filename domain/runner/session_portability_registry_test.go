package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type portStub struct {
	backend string
	calls   []string
}

func (p *portStub) Export(_ context.Context, sessionID, workDir string) (engine.SessionBundle, error) {
	p.calls = append(p.calls, sessionID+"@"+workDir)
	data := []byte(p.backend + ":" + sessionID)
	return engine.SessionBundle{Backend: p.backend, SessionID: sessionID, HarnessVersion: "1", OriginWorkDir: workDir, Data: data, Checksum: engine.BundleChecksum(data)}, nil
}
func (p *portStub) Import(context.Context, engine.SessionBundle, string) (string, error) {
	return "", nil
}

func TestSessionPortabilityRegistry_ForReturnsRegisteredAdapter(t *testing.T) {
	claude, codex := &portStub{backend: "claude-code"}, &portStub{backend: "codex"}
	reg := runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": claude, "codex": codex})
	got, ok := reg.For("codex")
	require.True(t, ok)
	assert.Same(t, codex, got)
	got, ok = reg.For("claude-code")
	require.True(t, ok)
	assert.Same(t, claude, got)
}
func TestSessionPortabilityRegistry_CopiesInput(t *testing.T) {
	codex := &portStub{backend: "codex"}
	src := map[string]engine.SessionPortability{"codex": codex}
	reg := runner.NewSessionPortabilityRegistry(src)
	delete(src, "codex")
	src["pi"] = &portStub{backend: "pi"}
	_, ok := reg.For("codex")
	assert.True(t, ok)
	_, ok = reg.For("pi")
	assert.False(t, ok)
}
func TestSessionPortabilityRegistry_UnknownBackendIsAbsent(t *testing.T) {
	reg := runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": &portStub{backend: "claude-code"}})
	for _, b := range []string{"opencode", "pi", "", "CLAUDE-CODE"} {
		_, ok := reg.For(b)
		assert.False(t, ok, b)
	}
}
func TestSessionPortabilityRegistry_NilRegistryIsEmpty(t *testing.T) {
	var reg *runner.SessionPortabilityRegistry
	_, ok := reg.For("codex")
	assert.False(t, ok)
	_, ok = runner.NewSessionPortabilityRegistry(nil).For("codex")
	assert.False(t, ok)
}
func TestSessionPortabilityRegistry_DropsNilAndEmptyEntries(t *testing.T) {
	reg := runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"codex": nil, "": &portStub{backend: "x"}})
	_, ok := reg.For("codex")
	assert.False(t, ok, "nil adapters are not registered")
	_, ok = reg.For("")
	assert.False(t, ok, "an empty backend name is not registered")
}
