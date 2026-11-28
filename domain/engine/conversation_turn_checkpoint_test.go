package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stepLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *stepLog) add(s string) { l.mu.Lock(); defer l.mu.Unlock(); l.steps = append(l.steps, s) }
func (l *stepLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

type ckExporter struct {
	log  *stepLog
	err  error
	reqs []engine.SessionExportRequest
}

func (e *ckExporter) ExportSession(_ context.Context, req engine.SessionExportRequest) (engine.SessionBundle, error) {
	e.reqs = append(e.reqs, req)
	e.log.add("export:" + req.SessionID)
	if e.err != nil {
		return engine.SessionBundle{}, e.err
	}
	data := []byte("transcript " + req.SessionID)
	return engine.SessionBundle{Backend: req.Backend, SessionID: req.SessionID, HarnessVersion: "2.1.0", OriginWorkDir: req.WorkDir, Data: data, Checksum: engine.BundleChecksum(data)}, nil
}

type ckStore struct {
	log          *stepLog
	err          error
	keys         []engine.SessionKey
	refs, pruned []string
}

func (s *ckStore) Put(_ context.Context, k engine.SessionKey, b engine.SessionBundle) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	ref := fmt.Sprintf("session:%s/%s/%d/%s", k.RunID, k.InvocationID, k.TurnIndex, b.Checksum)
	s.keys, s.refs = append(s.keys, k), append(s.refs, ref)
	s.log.add("put")
	return ref, nil
}
func (s *ckStore) Get(context.Context, string) (engine.SessionBundle, error) {
	panic("checkpoint gate never reads a previously stored bundle")
}
func (s *ckStore) Prune(_ context.Context, _, _ uuid.UUID, keep string) error {
	s.pruned = append(s.pruned, keep)
	s.log.add("prune")
	return nil
}

type ckCommitter struct {
	log     *stepLog
	err     error
	commits []engine.TurnCommit
	apply   func(*instance.NodeExecution) error
	finish  func(uuid.UUID)
}

func (c *ckCommitter) CommitTurn(_ context.Context, tc engine.TurnCommit) error {
	c.log.add("commit")
	if c.err != nil {
		return c.err
	}
	exec := *tc.Execution
	conv := make(map[string]any, len(exec.LLMConversation))
	for k, v := range exec.LLMConversation {
		conv[k] = v
	}
	exec.LLMConversation = conv
	if tc.CheckpointRef != "" {
		ref := tc.CheckpointRef
		exec.SessionCheckpointRef = &ref
	}
	if tc.PinRunner != "" {
		pin := tc.PinRunner
		exec.SessionPinRunner = &pin
	}
	exec.SessionGeneration, exec.SessionInvocationID = tc.SessionGeneration, tc.InvocationID
	tc.Execution = &exec
	if c.apply != nil {
		if err := c.apply(&exec); err != nil {
			return err
		}
	}
	c.commits = append(c.commits, tc)
	if c.finish != nil {
		c.finish(tc.IntentID)
	}
	return nil
}

type ckWorkspace struct {
	log    *stepLog
	err    error
	staged []engine.TurnWorkspaceRequest
}

func (w *ckWorkspace) StageTurn(_ context.Context, req engine.TurnWorkspaceRequest) (engine.StagedTurnWorkspace, error) {
	w.staged = append(w.staged, req)
	w.log.add("stage-workspace")
	if w.err != nil {
		return engine.StagedTurnWorkspace{}, w.err
	}
	if req.Completion.Workspace == nil {
		return engine.StagedTurnWorkspace{}, engine.ErrInvalidStepCompletion
	}
	return engine.StagedTurnWorkspace{RunID: req.RunID, RunnerID: req.RunnerID, Version: 7, Ref: req.Completion.Workspace.Ref, Completion: *req.Completion.Workspace}, nil
}

type ckFixture struct {
	log       *stepLog
	exporter  *ckExporter
	store     *ckStore
	committer *ckCommitter
	workspace *ckWorkspace
}

func (f ckFixture) deps() engine.SessionCheckpointDeps {
	return engine.SessionCheckpointDeps{Store: f.store, Exporter: f.exporter, Committer: f.committer, Workspace: f.workspace, DefaultAgentBackend: "claude-code"}
}
func newCkFixture() ckFixture {
	log := &stepLog{}
	return ckFixture{log: log, exporter: &ckExporter{log: log}, store: &ckStore{log: log}, committer: &ckCommitter{log: log}, workspace: &ckWorkspace{log: log}}
}

type sessionCheckpointSetter interface {
	SetSessionCheckpoints(engine.SessionCheckpointDeps)
}
type memIntents struct {
	mu        sync.Mutex
	persisted []engine.TurnIntent
	completed []uuid.UUID
	committed map[uuid.UUID]bool
}

func (m *memIntents) Persist(_ context.Context, in engine.TurnIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persisted = append(m.persisted, in)
	return nil
}
func (m *memIntents) Complete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, id)
	return nil
}
func (m *memIntents) Pending(context.Context) ([]engine.TurnIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pending []engine.TurnIntent
	for _, in := range m.persisted {
		if !m.committed[in.ID] {
			pending = append(pending, in)
		}
	}
	return pending, nil
}
func (m *memIntents) PendingFor(ctx context.Context, key string) (*engine.TurnIntent, error) {
	pending, err := m.Pending(ctx)
	for _, in := range pending {
		if in.LeaseKey == key {
			return &in, nil
		}
	}
	return nil, err
}
func (m *memIntents) committedByTransaction(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.committed == nil {
		m.committed = make(map[uuid.UUID]bool)
	}
	m.committed[id] = true
}

type bindingResolver struct{ b provider.ResolvedBinding }

func (r bindingResolver) Resolve(context.Context, string) (provider.ResolvedBinding, error) {
	return r.b, nil
}
func setWorkspaceVar(repo *mockInstanceRepo, id uuid.UUID, dir string) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.instances[id].Variables[engine.ReservedVarWorkspace] = dir
}
func assistantEvents(bus *mockEventBus, id uuid.UUID) int {
	n := 0
	for _, e := range bus.eventsOfType(id, engine.EventTypeInteractiveMessage) {
		if data, ok := e.Data.(map[string]any); ok && data["role"] == "assistant" {
			n++
		}
	}
	return n
}

// Like the dispatcher, record the exact completion before forwarding Done.
func receiptStream(runnerID string, workspace bool, sessionID string, tokens ...string) func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		sink, ok := engine.StepCompletionSinkFromContext(ctx)
		if !ok {
			return nil, errors.New("test: no completion sink installed for this turn")
		}
		c := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: runnerID, Generation: 1}}
		if workspace {
			id, ok := engine.StepIdentityFromContext(ctx)
			if !ok {
				return nil, errors.New("test: no turn identity")
			}
			c.Workspace = &engine.WorkspaceCompletion{Ref: id.RunID.String() + "@7:" + strings.Repeat("a", 64), Resource: engine.ResourceFenceRef{Key: "run:" + id.RunID.String(), Owner: runnerID, Generation: 1, Token: uuid.New()}}
		}
		if err := sink.Record(c); err != nil {
			return nil, err
		}
		return cannedStreamWithSession(tokens, sessionID)(ctx, req)
	}
}
func wiredHandler(t *testing.T, h interface{}, f ckFixture) *memIntents {
	t.Helper()
	intents := &memIntents{}
	f.committer.finish = intents.committedByTransaction
	h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute)
	h.(turnIntentSetter).SetTurnIntents(intents)
	h.(invocationSettable).SetInvocationStore(engine.NewMemoryInvocationStore())
	h.(sessionCheckpointSetter).SetSessionCheckpoints(f.deps())
	return intents
}
func TestConversationHandler_TurnCheckpoint_AgentTurnCommitsExactPair(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
	setWorkspaceVar(instRepo, id, "/srv/ws/run-1")
	h := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(llm), bindingResolver{b: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: interactiveChatModel, Backend: "codex"}}, bus, queue)
	f := newCkFixture()
	intents := wiredHandler(t, h, f)
	llm.streamCallFn = receiptStream("mac-a", true, "thread-1", "edited")
	require.NoError(t, h.HandleMessage(convoCtx(), id, "edit it"))
	assert.Equal(t, []string{"export:thread-1", "put", "stage-workspace", "commit", "prune"}, f.log.all())
	require.Len(t, f.exporter.reqs, 1)
	assert.Equal(t, "mac-a", f.exporter.reqs[0].RunnerID)
	assert.Equal(t, "codex", f.exporter.reqs[0].Backend)
	assert.Equal(t, "/srv/ws/run-1", f.exporter.reqs[0].WorkDir)
	assert.Equal(t, "/srv/ws/run-1", f.workspace.staged[0].WorkDir)
	require.Len(t, f.committer.commits, 1)
	c := f.committer.commits[0]
	require.Len(t, intents.persisted, 1)
	assert.Equal(t, intents.persisted[0].ID, c.IntentID)
	assert.Equal(t, intents.persisted[0].RequestID, c.RequestID)
	assert.Equal(t, intents.persisted[0].InvocationID, c.InvocationID)
	assert.NotEqual(t, uuid.Nil, c.InvocationID)
	assert.Equal(t, engine.RunLeaseKey(id), c.LeaseKey)
	assert.NotEqual(t, uuid.Nil, c.LeaseToken)
	assert.Equal(t, "mac-a", c.PinRunner)
	assert.True(t, c.RequireWorkspace)
	assert.Equal(t, c.Completions[0].JobFence, f.exporter.reqs[0].JobFence)
	assert.Equal(t, c.Completions[0].Workspace.Resource, *f.exporter.reqs[0].Resource)
	assert.Equal(t, id.String()+"@7:"+strings.Repeat("a", 64), c.Execution.LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef])
	assert.Equal(t, 1, assistantEvents(bus, id))
	assert.Empty(t, intents.completed, "only CommitTurn completes the intent")
}
func TestConversationHandler_TurnCheckpoint_StatelessChatAndEndSession(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
	h := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(llm), bindingResolver{b: provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "openrouter", Model: interactiveChatModel, ConnectionRef: "openrouter-main"}}, bus, queue)
	f := newCkFixture()
	wiredHandler(t, h, f)
	llm.streamCallFn = receiptStream("builtin", false, "", "ok")
	require.NoError(t, h.HandleMessage(convoCtx(), id, "next"))
	assert.Empty(t, f.exporter.reqs)
	require.Len(t, f.committer.commits, 1)
	assert.Empty(t, f.committer.commits[0].CheckpointRef)
	assert.Len(t, f.committer.commits[0].Completions, 1)
	_, err := h.EndSession(convoCtx(), id)
	require.NoError(t, err)
	require.Len(t, f.committer.commits, 2)
	assert.Empty(t, f.committer.commits[1].Completions)
	assert.Empty(t, f.exporter.reqs)
}
func TestConversationHandler_TurnCheckpoint_FailureKeepsPriorPairAndPendingIntent(t *testing.T) {
	for name, mut := range map[string]func(*ckFixture){"export": func(f *ckFixture) { f.exporter.err = errors.New("transcript not found") }, "stage": func(f *ckFixture) { f.workspace.err = errors.New("candidate missing") }, "commit": func(f *ckFixture) { f.committer.err = fmt.Errorf("lease row moved: %w", engine.ErrTurnFenced) }} {
		t.Run(name, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
			setWorkspaceVar(instRepo, id, "/srv/ws/run-1")
			h := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(llm), bindingResolver{b: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: interactiveChatModel, Backend: "codex"}}, bus, queue)
			f := newCkFixture()
			mut(&f)
			intents := wiredHandler(t, h, f)
			before, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), id, "interact")
			require.NoError(t, err)
			beforeMsgs := len(extractMessages(t, before.LLMConversation))
			llm.streamCallFn = receiptStream("mac-a", true, "thread-1", "edited")
			err = h.HandleMessage(convoCtx(), id, "edit it")
			require.ErrorContains(t, err, map[string]string{"export": "transcript not found", "stage": "candidate missing", "commit": "lease row moved"}[name])
			require.Len(t, intents.persisted, 1)
			assert.Empty(t, f.committer.commits)
			assert.Contains(t, f.log.all(), "export:thread-1")
			after, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), id, "interact")
			require.NoError(t, err)
			assert.Len(t, extractMessages(t, after.LLMConversation), beforeMsgs)
			assert.Zero(t, assistantEvents(bus, id))
			assert.Empty(t, intents.completed)
			pending, err := intents.Pending(context.Background())
			require.NoError(t, err)
			require.Len(t, pending, 1)
			assert.Equal(t, intents.persisted[0].ID, pending[0].ID)
			assert.Empty(t, f.store.pruned)
		})
	}
}
func TestConversationHandler_TurnCheckpoint_MissingReceiptIsExplicitError(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	f := newCkFixture()
	wiredHandler(t, h, f)
	llm.streamCallFn = cannedStreamWithSession([]string{"ok"}, "sess-2")
	require.ErrorIs(t, h.HandleMessage(convoCtx(), id, "next"), engine.ErrMissingTurnReceipt)
	assert.Empty(t, f.committer.commits)
	assert.Zero(t, assistantEvents(bus, id))
	h2 := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	assert.Panics(t, func() { h2.(sessionCheckpointSetter).SetSessionCheckpoints(engine.SessionCheckpointDeps{}) })
}
func TestConversationHandler_TurnCheckpoint_TurnsKeyByInvocationAndTurnIndex(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	f := newCkFixture()
	f.committer.apply = func(exec *instance.NodeExecution) error { return nodeExecRepo.Update(context.Background(), exec) }
	wiredHandler(t, h, f)
	llm.streamCallFn = receiptStream("builtin", false, "sess-2", "a")
	require.NoError(t, h.HandleMessage(convoCtx(), id, "one"))
	llm.streamCallFn = receiptStream("builtin", false, "sess-2", "b")
	require.NoError(t, h.HandleMessage(convoCtx(), id, "two"))
	require.Len(t, f.store.keys, 2)
	assert.Equal(t, f.store.keys[0].InvocationID, f.store.keys[1].InvocationID)
	assert.Equal(t, f.store.keys[0].TurnIndex+1, f.store.keys[1].TurnIndex)
}
