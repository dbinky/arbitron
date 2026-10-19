package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// REAL-EMITTER PROVENANCE for the run view. Nothing here publishes on the bus:
// the group launcher, coordinator and task executions emit every frame. The sink
// is the subtree WebSocket a live view consumes, joined late (after launch) so
// the frames prove the subscription follows a running group rather than replaying.
func TestGroupSubtreeStreamE2E_RealGroupEmittersReachTheParentSocket(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-crash": groupCrashManifest})
	srv := gsServeAPI(t, ds)
	gates := newGroupProcessGates(t)
	t.Cleanup(func() { gates.release(t, "slow") })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	parentID, _, err := ds.eng.Trigger(ctx, "grp-crash", map[string]any{"gateDir": gates.dir}, "normal")
	require.NoError(t, err)
	gates.reached(t, "slow")
	waitForState(t, ds, parentID, instance.StateWaiting)

	conn, resp, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+parentID.String()+"/ws?subtree=1", nil)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	require.NoError(t, err, "the group parent is active, so the subtree socket must open (HTTP %d)", code)
	defer conn.Close(websocket.StatusNormalClosure, "")

	gates.release(t, "slow")

	type frame struct {
		Type   string         `json:"type"`
		Source uuid.UUID      `json:"sourceInstanceId"`
		Data   map[string]any `json:"data"`
	}
	var frames []frame
	for {
		_, msg, rerr := conn.Read(ctx)
		if rerr != nil {
			require.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(rerr), "stream must end with a normal close: %v", rerr)
			break
		}
		var f frame
		require.NoError(t, json.Unmarshal(msg, &f))
		frames = append(frames, f)
	}
	require.NotEmpty(t, frames)
	assert.Equal(t, adapthttp.SubtreeInactiveEventType, frames[len(frames)-1].Type)

	run := groupRunOf(t, ds, parentID, "group-1")
	decided, taskFrames, childTerminals := -1, 0, 0
	for i, f := range frames {
		if f.Source == parentID {
			if f.Type == engine.EventTypeGroupDecided && fmt.Sprint(f.Data["groupRunId"]) == run.ID.String() && f.Data["nodeId"] == "group-1" {
				decided = i
			}
			continue
		}
		child, gerr := ds.instanceRepo.Get(ctx, f.Source)
		require.NoError(t, gerr)
		require.NotNil(t, child.ParentInstanceID)
		assert.Equal(t, parentID, *child.ParentInstanceID)
		taskFrames++
		if f.Type == engine.EventTypeInstanceCompleted {
			childTerminals++
		}
	}
	assert.GreaterOrEqual(t, decided, 0, "the coordinator's group.decided must arrive as a frame sourced from the parent")
	assert.GreaterOrEqual(t, childTerminals, 1, "a group task's own terminal event must stream through the parent's subscription")
	assert.Positive(t, taskFrames)
}

// Nested fixtures for the coordinator ↔ subtree-stream seam. The root's
// workflows group runs two children: "inner" holds its own branches group (the
// intermediate parent of a nested decision) and "sib" is an unrelated sibling
// subtree. Both hold a process gate so every socket is live across the decision.
const nestedStreamInnerManifest = `{
  "name": "grp-sse-inner",
  "description": "branches group inside a workflow task",
  "inputs": [{"name": "gateDir", "type": "string", "required": true}],
  "nodes": [
    {"id": "bg", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "quick", "entry": "quick-set"}, {"key": "slow", "entry": "slow-hold"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "quick-set", "type": "transform",
     "config": {"operations": [{"variable": "qv", "operation": "set", "value": "Q"}]}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", ` + "`" + `__GATE__` + "`" + `, "sh", "{{gateDir}}", "inner"]}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "inner_done", "operation": "set", "value": "yes"}]}},
    {"id": "bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_done", "operation": "set", "value": "no"}]}}
  ],
  "edges": [
    {"source": "bg", "target": "quick-set", "output": "task", "label": "quick"},
    {"source": "bg", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "bg", "target": "done", "output": "success"},
    {"source": "bg", "target": "bad", "output": "failure"}
  ]
}`

const nestedStreamSiblingManifest = `{
  "name": "grp-sse-sib",
  "description": "unrelated sibling subtree",
  "inputs": [{"name": "gateDir", "type": "string", "required": true}],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", ` + "`" + `__GATE__` + "`" + `, "sh", "{{gateDir}}", "sib"]}}
  ],
  "edges": []
}`

const nestedStreamRootManifest = `{
  "name": "grp-sse-root",
  "description": "workflows group whose tasks are a nested group and a sibling",
  "inputs": [{"name": "gateDir", "type": "string", "required": true}],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "workflows",
       "tasks": [
         {"key": "inner", "workflow": "grp-sse-inner", "inputs": {"gateDir": "{{gateDir}}"}},
         {"key": "sib", "workflow": "grp-sse-sib", "inputs": {"gateDir": "{{gateDir}}"}}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results"}}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

type subtreeTestFrame struct {
	Type   string         `json:"type"`
	Source uuid.UUID      `json:"sourceInstanceId"`
	Data   map[string]any `json:"data"`
}

// collectSubtreeFrames reads conn until it closes, publishing each frame as it
// arrives and the full list once the server ends the stream.
func collectSubtreeFrames(ctx context.Context, conn *websocket.Conn) (<-chan subtreeTestFrame, <-chan []subtreeTestFrame) {
	live := make(chan subtreeTestFrame, 1024)
	all := make(chan []subtreeTestFrame, 1)
	go func() {
		var frames []subtreeTestFrame
		defer func() { close(live); all <- frames }()
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
					frames = append(frames, subtreeTestFrame{Type: "test.read_error: " + err.Error()})
				}
				return
			}
			var f subtreeTestFrame
			if json.Unmarshal(msg, &f) != nil {
				f = subtreeTestFrame{Type: "test.bad_frame"}
			}
			frames = append(frames, f)
			live <- f
		}
	}()
	return live, all
}

// paired:20 — a REAL coordinator decision on a NESTED group. group.decided is
// published on the intermediate parent (the run whose graph holds the group),
// so the grandparent's and the intermediate parent's subtree sockets both get
// it as a group.* frame sourced from that parent (the client refetches on the
// "group." prefix, any source), and the sibling subtree's socket never does.
func TestGroupSubtreeStreamE2E_NestedDecisionReachesAncestorsNotSiblings(t *testing.T) {
	gate := strings.ReplaceAll(groupGateScript, `"`, `\"`)
	ds, _ := bootGroupExecStack(t, 6, map[string]string{
		"grp-sse-root":  nestedStreamRootManifest,
		"grp-sse-inner": strings.Replace(nestedStreamInnerManifest, "`__GATE__`", `"`+gate+`"`, 1),
		"grp-sse-sib":   strings.Replace(nestedStreamSiblingManifest, "`__GATE__`", `"`+gate+`"`, 1),
	})
	srv := gsServeAPI(t, ds)
	gates := newGroupProcessGates(t)
	t.Cleanup(func() { gates.release(t, "inner"); gates.release(t, "sib") })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rootID, _, err := ds.eng.Trigger(ctx, "grp-sse-root", map[string]any{"gateDir": gates.dir}, "normal")
	require.NoError(t, err)
	gates.reached(t, "inner")
	gates.reached(t, "sib")

	outer := groupRunOf(t, ds, rootID, "outer")
	innerID := groupRosterTask(t, outer, "inner").ChildInstanceID
	sibID := groupRosterTask(t, outer, "sib").ChildInstanceID
	require.NotEqual(t, uuid.Nil, innerID)
	require.NotEqual(t, uuid.Nil, sibID)
	waitForState(t, ds, innerID, instance.StateWaiting)
	bg := groupRunOf(t, ds, innerID, "bg")
	require.Equal(t, innerID, bg.ParentInstanceID)

	dial := func(id uuid.UUID) *websocket.Conn {
		conn, resp, derr := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+id.String()+"/ws?subtree=1", nil)
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		require.NoError(t, derr, "subtree socket on active %s must open (HTTP %d)", id, code)
		t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
		return conn
	}
	rootLive, rootAll := collectSubtreeFrames(ctx, dial(rootID))
	_, innerAll := collectSubtreeFrames(ctx, dial(innerID))
	_, sibAll := collectSubtreeFrames(ctx, dial(sibID))

	isBGDecided := func(f subtreeTestFrame) bool {
		return f.Type == engine.EventTypeGroupDecided && fmt.Sprint(f.Data["groupRunId"]) == bg.ID.String()
	}

	// Release only the nested group's slow task: the coordinator decides bg while
	// the sibling subtree is still active and its socket open.
	gates.release(t, "inner")
	sawDecision := false
	deadline := time.After(30 * time.Second)
wait:
	for {
		select {
		case f, ok := <-rootLive:
			if !ok {
				break wait
			}
			if isBGDecided(f) {
				sawDecision = true
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	require.True(t, sawDecision, "the nested group's decision must reach the grandparent socket before the sibling finishes")

	innerFrames := <-innerAll
	gates.release(t, "sib")
	sibFrames := <-sibAll
	go func() {
		for range rootLive {
		}
	}()
	rootFrames := <-rootAll

	assertDecision := func(name string, frames []subtreeTestFrame) {
		t.Helper()
		require.NotEmpty(t, frames, name)
		for _, f := range frames {
			require.False(t, strings.HasPrefix(f.Type, "test."), "%s socket: %s", name, f.Type)
		}
		assert.Equal(t, adapthttp.SubtreeInactiveEventType, frames[len(frames)-1].Type, name)
		n := 0
		for _, f := range frames {
			if fmt.Sprint(f.Data["groupRunId"]) != bg.ID.String() || !strings.HasPrefix(f.Type, "group.") {
				continue
			}
			assert.Equal(t, innerID, f.Source, "%s: %s for the nested group must be sourced from its parent, not a task", name, f.Type)
			assert.Equal(t, "bg", f.Data["nodeId"], name)
			if isBGDecided(f) {
				n++
				assert.Equal(t, "success", f.Data["outcome"], name)
			}
		}
		assert.Equal(t, 1, n, "%s socket must carry exactly one group.decided for the nested run", name)
	}
	assertDecision("grandparent", rootFrames)
	assertDecision("intermediate parent", innerFrames)

	// The sibling socket was live (it saw its own completion) yet carried
	// nothing from the nested subtree: not the decision, not any task event.
	require.NotEmpty(t, sibFrames)
	sawOwn := false
	for _, f := range sibFrames {
		require.False(t, strings.HasPrefix(f.Type, "test."), "sibling socket: %s", f.Type)
		if f.Source == sibID && f.Type == engine.EventTypeInstanceCompleted {
			sawOwn = true
		}
		assert.NotEqual(t, innerID, f.Source, "sibling socket received %s from the nested group's parent", f.Type)
		assert.NotEqual(t, rootID, f.Source, "sibling socket received %s from its own parent", f.Type)
		assert.NotEqual(t, bg.ID.String(), fmt.Sprint(f.Data["groupRunId"]), "sibling socket received nested group event %s", f.Type)
		if f.Source != sibID && f.Source != uuid.Nil {
			src, gerr := ds.instanceRepo.Get(ctx, f.Source)
			require.NoError(t, gerr)
			require.NotNil(t, src.ParentInstanceID, "sibling socket frame %s from foreign root %s", f.Type, f.Source)
			assert.Equal(t, sibID, *src.ParentInstanceID, "sibling socket frame %s from outside its subtree", f.Type)
		}
	}
	assert.True(t, sawOwn, "the sibling socket must have been live: its own instance.completed frame is missing")
}
