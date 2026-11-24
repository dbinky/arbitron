package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rwFakeClaudeScript stands in for the claude CLI. It answers --version and
// `auth status` (used by inventory and the auth prober) without logging. Every
// model call is logged to calls.log. When the `exhausted` file exists, a call
// prints the structured rejected-quota record (P1 T11 classifies it as
// exhausted, reset 2100-01-01) and fails. Otherwise a call blocks until
// `release-all` exists or it wins an atomic mkdir of `token`, so pre-creating
// `token` blocks every call and removing it lets exactly one call finish.
const rwFakeClaudeScript = `#!/bin/sh
case "$1" in
  --version) echo "2.1.330 (Claude Code)"; exit 0 ;;
  auth) echo '{"loggedIn":true}'; exit 0 ;;
esac
cat > /dev/null
echo "$*" >> '@DIR@/calls.log'
if [ -f '@DIR@/exhausted' ]; then
  echo '{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","resetsAt":4102444800,"rateLimitType":"seven_day"},"message":{"content":[]}}'
  echo '{"type":"result","is_error":true,"result":"usage limit reached","session_id":"s"}'
  exit 1
fi
while [ ! -f '@DIR@/release-all' ]; do
  if mkdir '@DIR@/token' 2>/dev/null; then break; fi
  sleep 0.05
done
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
echo '{"type":"result","is_error":false,"result":"hello","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"'"$m"'":{}}}'
`

func rwWriteWorkflows(t *testing.T, ws string) {
	t.Helper()
	writeWorkflow(t, ws, "rr-local", `{
  "name": "rr-local", "description": "graph + model + process", "inputs": [],
  "nodes": [
    {"id": "init", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "x", "operation": "set", "value": 1}]}},
    {"id": "ask", "type": "llm_call", "config": {"model_profile": "rr-chat", "templateFile": "templates/p.md"}},
    {"id": "run", "type": "script", "config": {"scriptFile": "scripts/emit.sh", "interpreter": "/bin/sh", "timeoutSeconds": 30}},
    {"id": "check", "type": "conditional",
     "config": {"conditions": [{"field": "x", "operator": "eq", "value": 1, "output": "yes"}], "defaultOutput": "no"}},
    {"id": "done", "type": "transform", "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}},
    {"id": "other", "type": "transform", "config": {"operations": [{"variable": "finished", "operation": "set", "value": false}]}}
  ],
  "edges": [
    {"source": "init", "target": "ask"}, {"source": "ask", "target": "run"}, {"source": "run", "target": "check"},
    {"source": "check", "target": "done", "output": "yes"}, {"source": "check", "target": "other", "output": "no"}
  ]
}`)
	writePinTemplate(t, ws, "rr-local")
	scripts := filepath.Join(ws, "users", localDefaultUser.String(), "rr-local", "scripts")
	require.NoError(t, os.MkdirAll(scripts, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(scripts, "emit.sh"), []byte("#!/bin/sh\nprintf '{\"answer\":42}\\n'\n"), 0o700))

	writeWorkflow(t, ws, "rr-block", `{"name": "rr-block", "inputs": [], "nodes": [
    {"id": "ask", "type": "llm_call", "entrypoint": true, "config": {"model_profile": "rr-chat", "templateFile": "templates/p.md"}}
  ], "edges": []}`)
	writePinTemplate(t, ws, "rr-block")

	writeWorkflow(t, ws, "rr-talk", `{"name": "rr-talk", "inputs": [], "nodes": [
    {"id": "talk", "type": "interactive_agent", "entrypoint": true,
     "config": {"model_profile": "rr-agent", "orchestrationFile": "templates/o.md", "templateFile": "templates/p.md"}}
  ], "edges": []}`)
	writePinTemplate(t, ws, "rr-talk")
	o := filepath.Join(ws, "users", localDefaultUser.String(), "rr-talk", "templates", "o.md")
	require.NoError(t, os.WriteFile(o, []byte("orchestrate"), 0o600))
}

// rwWriteRoot writes a hermetic root: local auth, queue caps above nine, the
// fake claude binary, and the runner step limit.
func rwWriteRoot(t *testing.T, stepLimit int) (configPath, fakeDir string) {
	t.Helper()
	pinSinkRootEnv(t)
	setenvForTest(t, "PATH", "/usr/bin:/bin") // no host codex/opencode/pi leak into the inventory
	setenvForTest(t, "HOME", t.TempDir())
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	rwWriteWorkflows(t, ws)
	fakeDir = filepath.Join(root, "fake")
	require.NoError(t, os.MkdirAll(fakeDir, 0o700))
	bin := filepath.Join(fakeDir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(strings.ReplaceAll(rwFakeClaudeScript, "@DIR@", fakeDir)), 0o700))
	cfg := "server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(ws) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 20\n  perusercap: 20\n" +
		"llm:\n  binarypath: " + yamlQuote(bin) + "\n" +
		"runners:\n  defaultsteplimit: " + strconv.Itoa(stepLimit) + "\n"
	configPath = filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	return configPath, fakeDir
}

// rwBoot builds the REAL composition root (not started: no recovery, no probe loop).
func rwBoot(t *testing.T, stepLimit int) (*domainStack, string) {
	t.Helper()
	configPath, fake := rwWriteRoot(t, stepLimit)
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	require.NotNil(t, ds.runners, "the root must assemble the runner pool")
	return ds, fake
}

func rwTouch(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
}

func rwCalls(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func rwChatProfile(t *testing.T, ctx context.Context, ds *domainStack) {
	t.Helper()
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-chat", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)
}

type rwJob struct {
	NodeID, Op, State, RunnerID, SourceID, InvocationID, OperationID string
}

// rwJobs reads the SINK: the runner_jobs rows of a run.
func rwJobs(t *testing.T, db *sql.DB, runID uuid.UUID) []rwJob {
	t.Helper()
	rows, err := db.Query(`SELECT node_id, op, state, COALESCE(runner_id, ''), COALESCE(source_id, ''), invocation_id, operation_id
		FROM runner_jobs WHERE run_id = ? ORDER BY node_id, operation_id`, runID.String())
	require.NoError(t, err)
	defer rows.Close()
	var out []rwJob
	for rows.Next() {
		var j rwJob
		require.NoError(t, rows.Scan(&j.NodeID, &j.Op, &j.State, &j.RunnerID, &j.SourceID, &j.InvocationID, &j.OperationID))
		out = append(out, j)
	}
	require.NoError(t, rows.Err())
	return out
}

func rwCountJobs(db *sql.DB, where string) int {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE ` + where).Scan(&n); err != nil {
		return -1
	}
	return n
}

// rwEveryOpPlacedOnBuiltin is the local-run sink predicate: exactly one row per
// expected node with the expected op, each completed on the built-in runner,
// and no row for any other node.
func rwEveryOpPlacedOnBuiltin(jobs []rwJob, want map[string]string) bool {
	if len(jobs) != len(want) {
		return false
	}
	for _, j := range jobs {
		op, ok := want[j.NodeID]
		if !ok || j.Op != op || j.State != "completed" || j.RunnerID != string(runner.BuiltInRunnerID) {
			return false
		}
	}
	return true
}

func rwExecutionOf(ctx context.Context, ds *domainStack, id uuid.UUID) (instance.ExecutionStatus, bool) {
	rows, err := ds.eng.ListEnriched(ctx, instance.ListFilters{})
	if err != nil {
		return instance.ExecutionStatus{}, false
	}
	for _, r := range rows {
		if r.ID == id && len(r.Execution) == 1 {
			return r.Execution[0], true
		}
	}
	return instance.ExecutionStatus{}, false
}

func rwSourceHealth(t *testing.T, ds *domainStack, id runner.SourceID) runner.SourceHealth {
	t.Helper()
	src, err := ds.runners.registry.GetSource(context.Background(), id)
	require.NoError(t, err)
	return src.Health
}

var rwAnthropic = runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "")

// Happy path (design §8): the local run completes with the exact outputs. Every
// provider/process op has a completed runner_jobs row on builtin and the trace
// names builtin. Graph/control nodes have no row.
func TestRunnerWiring_LocalRunPlacesOnBuiltin(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)

	id, _, err := ds.eng.Trigger(ctx, "rr-local", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

	execs, err := ds.nodeExecRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	byNode := map[string]instance.NodeExecution{}
	for _, e := range execs {
		byNode[e.NodeID] = e
	}
	assert.Equal(t, map[string]any{"response": "hello"}, byNode["ask"].Output)
	assert.Equal(t, map[string]any{"answer": float64(42)}, byNode["run"].Output)
	for _, n := range []string{"ask", "run"} {
		require.NotNil(t, byNode[n].RunnerID, n)
		assert.Equal(t, "builtin", *byNode[n].RunnerID, n)
	}
	for _, n := range []string{"init", "check", "done"} {
		assert.Nil(t, byNode[n].RunnerID, "graph node %s is never placed", n)
	}

	jobs := rwJobs(t, ds.db, id)
	assert.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"ask": "chat", "run": "process"}), "%+v", jobs)
	for _, j := range jobs {
		switch j.NodeID {
		case "ask":
			assert.Equal(t, string(rwAnthropic), j.SourceID)
		case "run":
			assert.Empty(t, j.SourceID, "process assignments reserve a runner slot without a credential source")
		}
	}
	assert.Equal(t, 1, rwCalls(fake), "one harness call for one llm_call")
}

// Built-in runner and its sources through the real MCP and REST surfaces.
func TestRunnerWiring_MCPRunnerListShowsBuiltin(t *testing.T) {
	ds, _ := rwBoot(t, 8)
	ctx := adminCtxForLocalUser(t, ds)
	isErr, text := mcpToolCallerAs(t, ds, ctx)("runner_list", map[string]any{})
	require.False(t, isErr, text)
	var body struct {
		Runners []struct {
			ID              string            `json:"id"`
			BuiltIn         bool              `json:"builtIn"`
			StepLimit       int               `json:"stepLimit"`
			HarnessVersions map[string]string `json:"harnessVersions"`
			Sources         []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"sources"`
		} `json:"runners"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &body))
	require.Len(t, body.Runners, 1)
	r := body.Runners[0]
	assert.Equal(t, "builtin", r.ID)
	assert.True(t, r.BuiltIn)
	assert.Equal(t, 8, r.StepLimit)
	assert.Equal(t, "2.1.330", r.HarnessVersions["claude-code"], "inventory ran the configured claude binary")
	var ids []string
	for _, s := range r.Sources {
		ids = append(ids, s.ID)
	}
	assert.Contains(t, ids, "builtin/anthropic_subscription/-")
	assert.Contains(t, ids, "builtin/openai_subscription/-")
	assert.Contains(t, ids, "builtin/none/-")

	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/v1/runners")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var rest, mcp any
	require.NoError(t, json.Unmarshal(raw, &rest))
	require.NoError(t, json.Unmarshal([]byte(text), &mcp))
	assert.Equal(t, mcp, rest, "REST and MCP render the same pool")
}

// A connection written through the admin service is a built-in source at once,
// and disappears when deleted.
func TestRunnerWiring_ConnectionChangeResyncsSources(t *testing.T) {
	ds, _ := rwBoot(t, 8)
	ctx := adminCtxForLocalUser(t, ds)
	want := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "rr-conn")
	ids := func() []runner.SourceID {
		_, srcs, err := ds.runners.registry.GetRunner(ctx, runner.BuiltInRunnerID)
		require.NoError(t, err)
		out := make([]runner.SourceID, 0, len(srcs))
		for _, s := range srcs {
			out = append(out, s.ID)
		}
		return out
	}
	require.NotContains(t, ids(), want)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "rr-conn", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, "key"))
	assert.Contains(t, ids(), want)
	require.NoError(t, ds.connSvc.Delete(ctx, "rr-conn"))
	assert.NotContains(t, ids(), want)
}

type rwStatus struct {
	Run   uuid.UUID
	State instance.ExecutionState
}

// rwCollectStatuses records every execution.status event published on the bus.
func rwCollectStatuses(t *testing.T, bus engine.EventBus) func() []rwStatus {
	t.Helper()
	ch, unsubscribe := bus.SubscribeAll()
	var mu sync.Mutex
	var got []rwStatus
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			if ev.Event.Type != runner.EventExecutionStatus {
				continue
			}
			if st, ok := ev.Event.Data.(instance.ExecutionStatus); ok {
				mu.Lock()
				got = append(got, rwStatus{Run: ev.InstanceID, State: st.State})
				mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { unsubscribe(); <-done })
	return func() []rwStatus {
		mu.Lock()
		defer mu.Unlock()
		return append([]rwStatus(nil), got...)
	}
}

// Edge (design §8): with step limit 8, nine concurrent blocking chat steps run
// eight; the ninth is queued_slot (ledger, enrichment, bus) and starts when a
// slot frees.
func TestRunnerWiring_StepLimitQueuesNinth(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	require.NoError(t, os.Mkdir(filepath.Join(fake, "token"), 0o700)) // every harness call blocks
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	statuses := rwCollectStatuses(t, ds.bus)

	ids := make([]uuid.UUID, 0, 9)
	for i := 0; i < 9; i++ {
		id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Eventually(t, func() bool {
		return rwCountJobs(ds.db, "state IN ('offered', 'running')") == 8 &&
			rwCountJobs(ds.db, "state = 'queued'") == 1 && rwCalls(fake) == 8
	}, 30*time.Second, 50*time.Millisecond, "exactly eight steps hold slots and the ninth queues")
	assert.Equal(t, 8, rwCalls(fake), "the ninth never reached the harness")

	var ninthRaw string
	require.NoError(t, ds.db.QueryRow(`SELECT run_id FROM runner_jobs WHERE state = 'queued'`).Scan(&ninthRaw))
	ninth := uuid.MustParse(ninthRaw)
	st, ok := rwExecutionOf(ctx, ds, ninth)
	require.True(t, ok, "the queued step is projected into instance enrichment")
	assert.Equal(t, instance.ExecQueuedSlot, st.State)
	inst, err := ds.instanceRepo.Get(ctx, ninth)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, inst.State, "a slot wait never changes the run state")

	require.NoError(t, os.Remove(filepath.Join(fake, "token"))) // exactly one blocked call may finish
	require.Eventually(t, func() bool {
		return rwCountJobs(ds.db, "run_id = '"+ninth.String()+"' AND state IN ('offered', 'running')") == 1 && rwCalls(fake) == 9
	}, 30*time.Second, 50*time.Millisecond, "the ninth step starts when a slot frees")
	assert.Equal(t, 8, rwCountJobs(ds.db, "state IN ('offered', 'running')"), "never more than eight")
	require.Eventually(t, func() bool {
		var seq []instance.ExecutionState
		for _, s := range statuses() {
			if s.Run == ninth {
				seq = append(seq, s.State)
			}
		}
		q := slices.Index(seq, instance.ExecQueuedSlot)
		r := slices.Index(seq, instance.ExecRunning)
		return q >= 0 && r > q
	}, 10*time.Second, 50*time.Millisecond, "the bus carries queued_slot then running for the ninth step")

	rwTouch(t, fake, "release-all")
	for _, id := range ids {
		assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	}
	assert.Equal(t, 0, rwCountJobs(ds.db, "state IN ('queued', 'offered', 'running')"))
}

// Failure + Error (design §8): a classified-exhausted claude error parks the
// source (reset kept), the step waits · capacity naming the group and reset,
// the source is never retried, and cancelling the run ends the wait.
func TestRunnerWiring_ExhaustedSourceWaitsCapacityAndCancelEndsWait(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "exhausted")
	rwTouch(t, fake, "release-all")
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	reset := time.Unix(4102444800, 0).UTC()

	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return rwSourceHealth(t, ds, rwAnthropic).Capacity == runner.CapacityExhausted
	}, 30*time.Second, 50*time.Millisecond, "the classified outcome reaches source health")
	h := rwSourceHealth(t, ds, rwAnthropic)
	require.NotNil(t, h.ResetAt)
	assert.True(t, reset.Equal(*h.ResetAt), "reset %v", h.ResetAt)

	var st instance.ExecutionStatus
	require.Eventually(t, func() bool {
		var ok bool
		st, ok = rwExecutionOf(ctx, ds, id)
		return ok && st.State == instance.ExecWaitingCapacity
	}, 30*time.Second, 50*time.Millisecond, "the step waits on capacity")
	assert.Equal(t, "anthropic_subscription:anthropic", st.Group)
	require.NotNil(t, st.NextReset)
	assert.True(t, reset.Equal(*st.NextReset))
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, inst.State, "a capacity wait never changes the run state")
	assert.Equal(t, 1, rwCalls(fake), "an exhausted source is never retried")

	require.NoError(t, ds.eng.Cancel(ctx, id))
	assert.Equal(t, instance.StateCancelled, waitTerminal(t, ds, id).State)
	require.Eventually(t, func() bool {
		return rwCountJobs(ds.db, "run_id = '"+id.String()+"' AND state IN ('queued', 'offered', 'running')") == 0
	}, 10*time.Second, 50*time.Millisecond, "a cancelled wait leaves no active job")
	_, stillListed := rwExecutionOf(ctx, ds, id)
	assert.False(t, stillListed, "no stale wait is projected for a cancelled run")
}

// Proves the local-run sink predicate detects a missing decorator. The same
// call through the inner (undecorated) selector leaves no runner_jobs row, so
// rwEveryOpPlacedOnBuiltin, and with it TestRunnerWiring_LocalRunPlacesOnBuiltin,
// fails if the root stops wrapping providerFactory. It also asserts the
// read-only refresh path keeps the undecorated selectors.
func TestRunnerWiring_DecoratorRemovalBreaksRouting(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	b, err := ds.profileResolver.Resolve(ctx, "rr-chat")
	require.NoError(t, err)

	runID := uuid.New()
	call := func(sel engine.ChatProviderSelector) string {
		inv := uuid.New()
		stepCtx := engine.ContextWithStepIdentity(ctx, engine.StepIdentity{
			RunID: runID, NodeID: "ask", InvocationID: inv, OperationID: "ask", Kind: engine.OperationNode, Attempt: 1,
		})
		p, err := sel.ChatProviderFor(b)
		require.NoError(t, err)
		resp, err := p.Chat(stepCtx, engine.ChatRequest{Model: b.Model, Prompt: "hello", WorkDir: t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, "hello", resp.Output, "routing never changes the provider's output")
		return inv.String()
	}
	byInvocation := func(inv string) []rwJob {
		var out []rwJob
		for _, j := range rwJobs(t, ds.db, runID) {
			if j.InvocationID == inv {
				out = append(out, j)
			}
		}
		return out
	}
	routed := call(ds.runners.chats)
	inner := call(ds.runners.innerChats)
	assert.True(t, rwEveryOpPlacedOnBuiltin(byInvocation(routed), map[string]string{"ask": "chat"}), "the root's selector is routed")
	assert.False(t, rwEveryOpPlacedOnBuiltin(byInvocation(inner), map[string]string{"ask": "chat"}), "an undecorated selector leaves no sink row")
	assert.Empty(t, byInvocation(inner))

	assert.True(t, ds.refreshDeps.Chats == ds.runners.innerChats, "model refresh probes keep the undecorated chat selector")
	assert.True(t, ds.refreshDeps.Agents == ds.runners.innerAgents, "model refresh probes keep the undecorated agent selector")
	assert.True(t, ds.refreshDeps.Streams == ds.runners.innerStreams, "model refresh probes keep the undecorated stream selector")
	assert.False(t, ds.runners.chats == ds.runners.innerChats)
}

// The monitor runs only once the stack starts (after crash recovery). Its
// read-only auth probe consumes no slot and writes no job.
func TestRunnerWiring_MonitorProbesAfterStart(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	before := rwSourceHealth(t, ds, rwAnthropic)
	assert.Equal(t, runner.AuthUnknown, before.Auth, "nothing probes before Start")
	assert.Nil(t, before.LastProbeAt)

	require.NoError(t, ds.Start(context.Background()))
	require.Eventually(t, func() bool {
		h := rwSourceHealth(t, ds, rwAnthropic)
		return h.Auth == runner.AuthOK && h.LastProbeAt != nil
	}, 20*time.Second, 50*time.Millisecond, "the claude auth probe ran against the configured binary")
	assert.Equal(t, 0, rwCountJobs(ds.db, "1 = 1"), "read-only probes consume no slots")
	assert.Equal(t, 0, rwCalls(fake), "probes never spend a model call")
}

// Error (design §8, interactive stamping site, promised by P1 T2): the first
// turn and a follow-up are dispatched under the node's OPEN durable invocation,
// on builtin, as distinct operations.
func TestRunnerWiring_InteractiveTurnKeyedByOpenInvocation(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "rr-talk", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(ctx, id)
		return gerr == nil && inst.State == instance.StateWaiting
	}, 20*time.Second, 50*time.Millisecond, "the run parks at the interactive node")
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "follow-up"))

	var open string
	require.NoError(t, ds.db.QueryRow(`SELECT id FROM node_invocations WHERE run_id = ? AND node_id = 'talk' AND committed_at IS NULL`, id.String()).Scan(&open))
	require.Eventually(t, func() bool {
		jobs := rwJobs(t, ds.db, id)
		if len(jobs) != 2 {
			return false
		}
		for _, j := range jobs {
			if j.State != "completed" {
				return false
			}
		}
		return true
	}, 20*time.Second, 50*time.Millisecond, "first turn + follow-up each completed one job")
	jobs := rwJobs(t, ds.db, id)
	for _, j := range jobs {
		assert.Equal(t, open, j.InvocationID, "turns key under the open visit")
		assert.Equal(t, "stream", j.Op)
		assert.Equal(t, "builtin", j.RunnerID)
		assert.Equal(t, string(rwAnthropic), j.SourceID)
	}
	assert.NotEqual(t, jobs[0].OperationID, jobs[1].OperationID, "each turn is its own operation")
}
