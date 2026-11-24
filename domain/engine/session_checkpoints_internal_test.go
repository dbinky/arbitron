package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gateLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *gateLog) add(s string) { l.mu.Lock(); l.steps = append(l.steps, s); l.mu.Unlock() }
func (l *gateLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

type gateExporter struct {
	log    *gateLog
	err    error
	bundle func(SessionExportRequest) SessionBundle
	reqs   []SessionExportRequest
}

func (e *gateExporter) ExportSession(_ context.Context, r SessionExportRequest) (SessionBundle, error) {
	e.reqs = append(e.reqs, r)
	e.log.add("export")
	if e.err != nil {
		return SessionBundle{}, e.err
	}
	if e.bundle != nil {
		return e.bundle(r), nil
	}
	data := []byte("bundle:" + r.SessionID)
	return SessionBundle{Backend: r.Backend, SessionID: r.SessionID, HarnessVersion: "1", OriginWorkDir: r.WorkDir,
		Data: data, Checksum: BundleChecksum(data)}, nil
}

type gateStore struct {
	log    *gateLog
	err    error
	keys   []SessionKey
	pruned []string
}

func (s *gateStore) Put(_ context.Context, k SessionKey, b SessionBundle) (string, error) {
	s.log.add("put")
	if s.err != nil {
		return "", s.err
	}
	s.keys = append(s.keys, k)
	return fmt.Sprintf("session:%s/%s/%d/%s", k.RunID, k.InvocationID, k.TurnIndex, b.Checksum), nil
}
func (s *gateStore) Get(context.Context, string) (SessionBundle, error) { return SessionBundle{}, nil }
func (s *gateStore) Prune(_ context.Context, _, _ uuid.UUID, keep string) error {
	s.log.add("prune")
	s.pruned = append(s.pruned, keep)
	return nil
}

type gateCommitter struct {
	log *gateLog
	err error
	got []TurnCommit
}

func (c *gateCommitter) CommitTurn(_ context.Context, tc TurnCommit) error {
	c.log.add("commit")
	if c.err != nil {
		return c.err
	}
	c.got = append(c.got, tc)
	return nil
}

type gateWorkspace struct {
	log  *gateLog
	err  error
	reqs []TurnWorkspaceRequest
}

func (w *gateWorkspace) StageTurn(_ context.Context, req TurnWorkspaceRequest) (StagedTurnWorkspace, error) {
	w.log.add("stage")
	w.reqs = append(w.reqs, req)
	if w.err != nil {
		return StagedTurnWorkspace{}, w.err
	}
	ref := req.RunID.String() + "@2:" + strings.Repeat("a", 64)
	return StagedTurnWorkspace{RunID: req.RunID, RunnerID: req.RunnerID, Version: 2, Ref: ref,
		Completion: WorkspaceCompletion{Ref: ref, Resource: req.Completion.Workspace.Resource}}, nil
}

type gate struct {
	log *gateLog
	d   SessionCheckpointDeps
	ex  *gateExporter
	st  *gateStore
	cm  *gateCommitter
	ws  *gateWorkspace
}

func newGate() gate {
	l := &gateLog{}
	g := gate{log: l, ex: &gateExporter{log: l}, st: &gateStore{log: l}, cm: &gateCommitter{log: l}, ws: &gateWorkspace{log: l}}
	g.d = SessionCheckpointDeps{Store: g.st, Exporter: g.ex, Committer: g.cm, Workspace: g.ws, DefaultAgentBackend: "claude-code"}
	return g
}

func gatePlan(kind provider.ProfileKind, backend, connection string) turnCheckpointPlan {
	b := provider.ResolvedBinding{Kind: kind, Backend: backend, Model: "m", ConnectionRef: connection}
	return turnCheckpointPlan{binding: b, accepted: AcceptedTurnBinding{Kind: kind, Model: "m", Backend: backend, ConnectionRef: connection},
		workDir: "/srv/ws/run", turnIndex: 2, intentID: uuid.New(), requestID: uuid.New(), invocationID: uuid.New(),
		kind: TurnIntentMessage, lease: TurnLease{Key: "run:x", Token: uuid.New(), Generation: 3}}
}

func gateExec(sid string) *instance.NodeExecution {
	return &instance.NodeExecution{ID: uuid.New(), InstanceID: uuid.New(), NodeID: "talk",
		LLMConversation: map[string]any{"sessionId": sid, "messages": []any{}}}
}

func receipt(runnerID string, workspace bool) StepCompletion {
	c := StepCompletion{JobFence: JobFenceRef{JobID: uuid.New(), RunnerID: runnerID, Generation: 4}}
	if workspace {
		c.Workspace = &WorkspaceCompletion{Ref: uuid.NewString() + "@2:" + strings.Repeat("a", 64),
			Resource: ResourceFenceRef{Key: "run:x", Owner: runnerID, Generation: 1, Token: uuid.New()}}
	}
	return c
}

func withReceipts(t *testing.T, cs ...StepCompletion) context.Context {
	t.Helper()
	sink := NewStepCompletionSink()
	for _, c := range cs {
		require.NoError(t, sink.Record(c))
	}
	return ContextWithStepCompletionSink(context.Background(), sink)
}

// Happy: an agent turn exports from the receipt's runner, stores, validates the
// prepared workspace receipt, commits ONCE with every identity field, then prunes.
func TestCheckpointAndCommit_AgentTurnExportsStoresStagesThenOneCommit(t *testing.T) {
	g := newGate()
	p := gatePlan(provider.ProfileKindAgent, "codex", "")
	exec := gateExec("thread-1")
	rc := receipt("mac-a", true)

	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, rc), exec, p))

	assert.Equal(t, []string{"export", "put", "stage", "commit", "prune"}, g.log.all())
	require.Len(t, g.ex.reqs, 1)
	assert.Equal(t, SessionExportRequest{RunnerID: "mac-a", Backend: "codex", SessionID: "thread-1", WorkDir: "/srv/ws/run",
		JobFence: rc.JobFence, Resource: &rc.Workspace.Resource}, g.ex.reqs[0], "export runs under the stream's own fence")
	assert.Equal(t, []SessionKey{{RunID: exec.InstanceID, InvocationID: p.invocationID, TurnIndex: 2}}, g.st.keys)
	require.Len(t, g.ws.reqs, 1)
	assert.Equal(t, TurnWorkspaceRequest{RunID: exec.InstanceID, RunnerID: "mac-a", WorkDir: "/srv/ws/run", Completion: rc}, g.ws.reqs[0])

	require.Len(t, g.cm.got, 1)
	c := g.cm.got[0]
	assert.Equal(t, "mac-a", c.PinRunner)
	assert.Equal(t, int64(3), c.SessionGeneration)
	assert.Equal(t, p.lease.Key, c.LeaseKey)
	assert.Equal(t, p.lease.Token, c.LeaseToken)
	assert.Equal(t, p.intentID, c.IntentID)
	assert.Equal(t, p.requestID, c.RequestID)
	assert.Equal(t, p.invocationID, c.InvocationID)
	assert.Equal(t, 2, c.TurnIndex)
	assert.Equal(t, exec.InstanceID, c.ResourceRunID)
	assert.True(t, c.RequireWorkspace)
	assert.Equal(t, p.accepted, c.AcceptedBinding)
	require.Len(t, c.Completions, 1)
	assert.Equal(t, rc.JobFence, c.Completions[0].JobFence)
	require.NotNil(t, c.Completions[0].Workspace)
	wsRef := exec.InstanceID.String() + "@2:" + strings.Repeat("a", 64)
	assert.Equal(t, wsRef, c.Completions[0].Workspace.Ref, "the staged candidate receipt reaches the commit")
	assert.Equal(t, strings.HasPrefix(c.CheckpointRef, "session:"), true)
	assert.Equal(t, wsRef, c.Execution.LLMConversation[ConversationKeyWorkspaceCheckpointRef])
	assert.Equal(t, p.accepted, c.Execution.LLMConversation[ConversationKeyAcceptedTurnBinding])
	assert.Equal(t, InteractiveTurnIdentity{RunID: exec.InstanceID, NodeID: "talk", InvocationID: p.invocationID,
		RequestID: p.requestID, TurnIndex: 2, Generation: 3}, c.Execution.LLMConversation[ConversationKeyTurnIdentity])
	assert.Equal(t, []string{c.CheckpointRef}, g.st.pruned)
	assert.Equal(t, wsRef, exec.LLMConversation[ConversationKeyWorkspaceCheckpointRef], "caller sees the committed record")
}

// Success: stateless API chat commits transcript + job fence with no bundle and no
// workspace; End commits the operation against the existing pair with no receipt.
func TestCheckpointAndCommit_StatelessChatAndEndOperationCommitWithoutBundle(t *testing.T) {
	g := newGate()
	p := gatePlan(provider.ProfileKindChat, "", "openrouter-main")
	rc := receipt("builtin", false)
	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, rc), gateExec(""), p))
	assert.Empty(t, g.ex.reqs, "API chat has no harness session")
	assert.Empty(t, g.ws.reqs)
	require.Len(t, g.cm.got, 1)
	assert.Empty(t, g.cm.got[0].CheckpointRef)
	assert.Equal(t, []StepCompletion{rc}, g.cm.got[0].Completions, "the stream job is still fenced and completed")
	assert.False(t, g.cm.got[0].RequireWorkspace)

	g2 := newGate()
	end := gatePlan(provider.ProfileKindAgent, "codex", "")
	end.kind = TurnIntentEnd
	exec := gateExec("thread-1")
	ref, pin := "session:prior", "mac-a"
	exec.SessionCheckpointRef, exec.SessionPinRunner = &ref, &pin
	require.NoError(t, g2.d.commitOperation(context.Background(), exec, end))
	assert.Equal(t, []string{"commit"}, g2.log.all(), "End never exports, stages or invents a stream fence")
	c := g2.cm.got[0]
	assert.Empty(t, c.Completions)
	assert.Equal(t, "session:prior", c.CheckpointRef)
	assert.Equal(t, "mac-a", c.PinRunner)
	assert.Equal(t, end.intentID, c.IntentID)
}

// Failure: export, checksum, identity, store and stage failures commit nothing,
// leave the caller's record unchanged and prune nothing.
func TestCheckpointAndCommit_ExportChecksumOrStageFailureCommitsNothing(t *testing.T) {
	for name, mut := range map[string]func(*gate){
		"export error": func(g *gate) { g.ex.err = errors.New("transcript not found") },
		"bad checksum": func(g *gate) {
			g.ex.bundle = func(r SessionExportRequest) SessionBundle {
				return SessionBundle{Backend: r.Backend, SessionID: r.SessionID, Data: []byte("x"), Checksum: "00"}
			}
		},
		"wrong session": func(g *gate) {
			g.ex.bundle = func(r SessionExportRequest) SessionBundle {
				d := []byte("x")
				return SessionBundle{Backend: r.Backend, SessionID: "other", Data: d, Checksum: BundleChecksum(d)}
			}
		},
		"wrong backend": func(g *gate) {
			g.ex.bundle = func(r SessionExportRequest) SessionBundle {
				d := []byte("x")
				return SessionBundle{Backend: "claude-code", SessionID: r.SessionID, Data: d, Checksum: BundleChecksum(d)}
			}
		},
		"store error": func(g *gate) { g.st.err = errors.New("disk full") },
		"stage error": func(g *gate) { g.ws.err = errors.New("candidate missing") },
	} {
		t.Run(name, func(t *testing.T) {
			g := newGate()
			mut(&g)
			exec := gateExec("thread-1")
			before := fmt.Sprint(exec.LLMConversation)
			err := g.d.checkpointAndCommit(withReceipts(t, receipt("mac-a", true)), exec, gatePlan(provider.ProfileKindAgent, "codex", ""))
			require.Error(t, err)
			assert.Empty(t, g.cm.got, "no commit: the turn did not happen")
			assert.Empty(t, g.st.pruned)
			assert.Equal(t, before, fmt.Sprint(exec.LLMConversation), "the caller's record is untouched")
		})
	}
}

// Error: missing provenance or an unknown backend is rejected before export or
// commit; nil deps are rejected at wiring; a committer error propagates.
func TestCheckpointAndCommit_MissingProvenanceRejectedBeforeCommit(t *testing.T) {
	assert.Error(t, SessionCheckpointDeps{}.validate())
	d := newGate().d
	d.DefaultAgentBackend = ""
	assert.Error(t, d.validate(), "default agent backend required")
	assert.NoError(t, newGate().d.validate())

	agent := func() turnCheckpointPlan { return gatePlan(provider.ProfileKindAgent, "codex", "") }
	cases := map[string]struct {
		ctx  func(*testing.T) context.Context
		plan func() turnCheckpointPlan
		is   error
	}{
		"no sink":    {func(*testing.T) context.Context { return context.Background() }, agent, ErrMissingTurnReceipt},
		"empty sink": {func(t *testing.T) context.Context { return withReceipts(t) }, agent, ErrMissingTurnReceipt},
		"two receipts": {func(t *testing.T) context.Context {
			return withReceipts(t, receipt("mac-a", true), receipt("mac-b", true))
		}, agent, ErrMissingTurnReceipt},
		"no workspace":   {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", false)) }, agent, ErrMissingTurnReceipt},
		"no intent":      {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", true)) }, func() turnCheckpointPlan { p := agent(); p.intentID = uuid.Nil; return p }, ErrMissingTurnReceipt},
		"no request":     {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", true)) }, func() turnCheckpointPlan { p := agent(); p.requestID = uuid.Nil; return p }, ErrMissingTurnReceipt},
		"no lease token": {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", true)) }, func() turnCheckpointPlan { p := agent(); p.lease.Token = uuid.Nil; return p }, ErrMissingTurnReceipt},
		"no invocation":  {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", true)) }, func() turnCheckpointPlan { p := agent(); p.invocationID = uuid.Nil; return p }, ErrMissingTurnReceipt},
		"zero fence": {func(t *testing.T) context.Context {
			r := receipt("mac-a", true)
			r.JobFence.Generation = 0
			return withReceipts(t, r)
		}, agent, ErrMissingTurnReceipt},
		"unknown backend": {func(t *testing.T) context.Context { return withReceipts(t, receipt("mac-a", true)) }, func() turnCheckpointPlan { return gatePlan(provider.ProfileKindAgent, "gemini-cli", "") }, ErrUnknownSessionBackend},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := newGate()
			err := g.d.checkpointAndCommit(tc.ctx(t), gateExec("thread-1"), tc.plan())
			require.ErrorIs(t, err, tc.is)
			assert.Empty(t, g.ex.reqs, "nothing exported")
			assert.Empty(t, g.cm.got, "nothing committed")
		})
	}

	g := newGate()
	g.cm.err = fmt.Errorf("lease moved: %w", ErrTurnFenced)
	err := g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), gateExec("s"), agent())
	require.ErrorIs(t, err, ErrTurnFenced)
	assert.Empty(t, g.st.pruned, "nothing is pruned without a commit: the prior pair stays")
}

// Edge: group staging never uses the parent's resource; an adopted turn's original
// receipt replaces the sink; a loop visit's new invocation keys a new bundle;
// opencode commits transcript+workspace with no native bundle.
func TestCheckpointAndCommit_GroupResourceAdoptedReceiptAndVisitIsolation(t *testing.T) {
	g := newGate()
	group := gatePlan(provider.ProfileKindAgent, "claude-code", "")
	group.group = true
	err := g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), gateExec("s"), group)
	require.ErrorIs(t, err, ErrMissingTurnReceipt, "group staging without its own resource never falls back to the parent workspace")
	assert.Empty(t, g.ws.reqs)

	g = newGate()
	staging := uuid.New()
	group.resourceID = staging
	exec := gateExec("s")
	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), exec, group))
	assert.Equal(t, staging, g.ws.reqs[0].RunID)
	assert.NotEqual(t, exec.InstanceID, g.ws.reqs[0].RunID)
	assert.Equal(t, staging, g.cm.got[0].ResourceRunID)
	assert.True(t, g.cm.got[0].RequireOpenSession)

	g = newGate()
	adopted := gatePlan(provider.ProfileKindAgent, "codex", "")
	orig := receipt("mac-a", true)
	adopted.completions = []StepCompletion{orig}
	require.NoError(t, g.d.checkpointAndCommit(context.Background(), gateExec("t"), adopted))
	assert.Equal(t, orig.JobFence, g.cm.got[0].Completions[0].JobFence, "the adopted turn commits its ORIGINAL receipt")

	g = newGate()
	visit1, visit2 := gatePlan(provider.ProfileKindAgent, "codex", ""), gatePlan(provider.ProfileKindAgent, "codex", "")
	e := gateExec("t")
	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), e, visit1))
	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), e, visit2))
	assert.NotEqual(t, g.st.keys[0].InvocationID, g.st.keys[1].InvocationID, "a back-edge visit never overwrites the previous visit's bundle")

	g = newGate()
	require.NoError(t, g.d.checkpointAndCommit(withReceipts(t, receipt("builtin", true)), gateExec("o"), gatePlan(provider.ProfileKindAgent, "opencode", "")))
	assert.Empty(t, g.ex.reqs, "opencode has no native bundle: a later move rebuilds visibly")
	assert.Len(t, g.ws.reqs, 1, "its workspace is still checkpointed")
}
