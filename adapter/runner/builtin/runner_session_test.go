package builtin_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recPortability struct {
	importedInto string
	importedData []byte
	importErr    error
}

func (*recPortability) Export(context.Context, string, string) (engine.SessionBundle, error) {
	panic("export must stay inside the originating stream assignment")
}

func (p *recPortability) Import(_ context.Context, b engine.SessionBundle, workDir string) (string, error) {
	if p.importErr != nil {
		return "", p.importErr
	}
	p.importedInto, p.importedData = workDir, b.Data
	return "imported-" + b.SessionID, nil
}

func sessionRunner(reg *runner.SessionPortabilityRegistry) *builtin.Runner {
	sel := &fakeSelectors{}
	return builtin.New(builtin.Deps{Chats: sel, Agents: sel, Streams: &fakeStreams{},
		Process: process.NewLocalProcessRunner(), Confirmations: &fakeConfirmations{}, Sessions: reg})
}

func TestBuiltinRunner_SessionImportUsesRegistryAdapterForBackend(t *testing.T) {
	claude, codex := &recPortability{}, &recPortability{}
	r := sessionRunner(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{
		"claude-code": claude, "codex": codex}))
	out, err := r.Run(context.Background(), assignment(), runner.StepJob{Op: runner.OpSessionImport,
		Session: &runner.SessionOp{Backend: "claude-code", WorkDir: "/ws/run", Bundle: &engine.SessionBundle{Backend: "claude-code", SessionID: "s1", Data: []byte("B")}}})
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	assert.Equal(t, "imported-s1", out.Session.SessionID)
	assert.Equal(t, "/ws/run", claude.importedInto)
	assert.Equal(t, []byte("B"), claude.importedData)
	assert.Empty(t, codex.importedInto, "the backend selects the adapter")
}

func TestBuiltinRunner_SessionOpUnknownBackendOrMissingRegistryFails(t *testing.T) {
	_, err := sessionRunner(nil).Run(context.Background(), assignment(), runner.StepJob{Op: runner.OpSessionImport,
		Session: &runner.SessionOp{Backend: "claude-code", WorkDir: "/ws/run", Bundle: &engine.SessionBundle{}}})
	require.Error(t, err)
	_, err = sessionRunner(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{})).Run(
		context.Background(), assignment(), runner.StepJob{Op: runner.OpSessionImport,
			Session: &runner.SessionOp{Backend: "pi", WorkDir: "/ws/run", Bundle: &engine.SessionBundle{}}})
	require.Error(t, err)
}

func TestBuiltinRunner_SessionImportRebuildErrorPropagates(t *testing.T) {
	claude := &recPortability{importErr: engine.ErrBundleRequiresRebuild}
	r := sessionRunner(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": claude}))
	_, err := r.Run(context.Background(), assignment(), runner.StepJob{Op: runner.OpSessionImport,
		Session: &runner.SessionOp{Backend: "claude-code", WorkDir: "/ws/run", Bundle: &engine.SessionBundle{SessionID: "s"}}})
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
}

func TestBuiltinRunner_SessionImportMalformedRequestNeverCallsAdapter(t *testing.T) {
	p := &recPortability{}
	r := sessionRunner(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": p}))
	for name, payload := range map[string]*runner.SessionOp{
		"missing payload":   nil,
		"missing bundle":    {Backend: "claude-code", WorkDir: "/ws"},
		"missing directory": {Backend: "claude-code", Bundle: &engine.SessionBundle{Backend: "claude-code"}},
		"wrong backend":     {Backend: "claude-code", WorkDir: "/ws", Bundle: &engine.SessionBundle{Backend: "codex"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.Run(context.Background(), assignment(), runner.StepJob{Op: runner.OpSessionImport, Session: payload})
			require.ErrorIs(t, err, builtin.ErrMalformedJob)
			assert.Empty(t, p.importedInto)
		})
	}
}
