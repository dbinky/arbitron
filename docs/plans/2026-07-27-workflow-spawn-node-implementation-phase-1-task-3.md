# Phase 1 — Task 3: tool-posture guard for the spawn node's LLM call

**Goal:** Map `spawn_workflow` to the chat profile kind in `expectedKindForNodeType` so a spawn node whose `model_profile` resolves to an agent (tool-enabled) binding fails closed with `ErrBindingKindMismatch` instead of silently skipping the guard.

**Architecture:** One case added to `expectedKindForNodeType` in `domain/engine/kind_guard.go`. That function returns `ok=false` for any unmapped type, which makes `assertBindingKind` a no-op — so a new node type that performs a chat call would silently skip the guard. Input preparation is a tool-suppressed reasoning call, so the spawn type maps to `provider.ProfileKindChat`, exactly as `llm_call` does. No other node type's mapping changes.

**Tech Stack:** Go 1.25.5, standard library `testing` + `errors`, module `github.com/arbitron/arbitron`.

---

### Task 3: `expectedKindForNodeType` spawn arm

**Files:**
- Modify: `domain/engine/kind_guard.go:31-38`
- Test:   `domain/engine/kind_guard_spawn_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/kind_guard_spawn_test.go`:

```go
package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TestExpectedKindForSpawnWorkflow: the spawn node performs a tool-SUPPRESSED
// reasoning call for input preparation, so its resolved binding must be chat.
func TestExpectedKindForSpawnWorkflow(t *testing.T) {
	kind, ok := expectedKindForNodeType(workflow.NodeTypeSpawnWorkflow)
	if !ok {
		t.Fatal("expectedKindForNodeType(spawn_workflow) returned ok=false; " +
			"an unmapped node type makes assertBindingKind a silent no-op")
	}
	if kind != provider.ProfileKindChat {
		t.Fatalf("expectedKindForNodeType(spawn_workflow) = %q, want %q", kind, provider.ProfileKindChat)
	}
}

// TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed: a spawn node whose
// profile resolves to an AGENT binding is a config-driven escalation from a
// tool-suppressed call to an RCE-capable one. It must fail closed.
func TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed(t *testing.T) {
	err := assertBindingKind("spawn-1", workflow.NodeTypeSpawnWorkflow, provider.ProfileKindAgent)
	if err == nil {
		t.Fatal("expected an error for an agent binding on a spawn_workflow node")
	}
	if !errors.Is(err, ErrBindingKindMismatch) {
		t.Fatalf("error is not ErrBindingKindMismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "spawn-1") {
		t.Fatalf("error must name the offending node id, got: %v", err)
	}
}

// TestAssertBindingKind_SpawnWithChatBinding_OK
func TestAssertBindingKind_SpawnWithChatBinding_OK(t *testing.T) {
	if err := assertBindingKind("spawn-1", workflow.NodeTypeSpawnWorkflow, provider.ProfileKindChat); err != nil {
		t.Fatalf("expected no error for a chat binding, got %v", err)
	}
}

// TestExpectedKindForNodeType_OtherMappingsUnchanged pins that this change is
// targeted: no other node type's mapping moved.
func TestExpectedKindForNodeType_OtherMappingsUnchanged(t *testing.T) {
	cases := []struct {
		t    workflow.NodeType
		kind provider.ProfileKind
		ok   bool
	}{
		{workflow.NodeTypeLLMCall, provider.ProfileKindChat, true},
		{workflow.NodeTypeInteractiveLLM, provider.ProfileKindChat, true},
		{workflow.NodeTypeAgentCall, provider.ProfileKindAgent, true},
		{workflow.NodeTypeInteractiveAgent, provider.ProfileKindAgent, true},
		{workflow.NodeTypeScript, "", false},
		{workflow.NodeTypeToolCall, "", false},
		{workflow.NodeTypeConditional, "", false},
		{workflow.NodeTypeTransform, "", false},
		{workflow.NodeTypeTimerDelay, "", false},
		{workflow.NodeTypeWebhookWait, "", false},
	}
	for _, c := range cases {
		kind, ok := expectedKindForNodeType(c.t)
		if ok != c.ok || kind != c.kind {
			t.Errorf("expectedKindForNodeType(%s) = (%q, %v), want (%q, %v)", c.t, kind, ok, c.kind, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run 'TestExpectedKindForSpawnWorkflow|TestAssertBindingKind_Spawn'
```

Expected output:

```
--- FAIL: TestExpectedKindForSpawnWorkflow (0.00s)
    kind_guard_spawn_test.go:...: expectedKindForNodeType(spawn_workflow) returned ok=false; an unmapped node type makes assertBindingKind a silent no-op
--- FAIL: TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed (0.00s)
    kind_guard_spawn_test.go:...: expected an error for an agent binding on a spawn_workflow node
FAIL	github.com/arbitron/arbitron/domain/engine	0.4s
```

- [ ] **Step 3: Minimal implementation**

Edit `domain/engine/kind_guard.go`, replacing the chat case (line 32-33):

```go
func expectedKindForNodeType(t workflow.NodeType) (provider.ProfileKind, bool) {
	switch t {
	// spawn_workflow performs a tool-SUPPRESSED input-preparation chat call, so
	// it belongs on the chat arm. Mapping it here is load-bearing, not
	// cosmetic: an UNMAPPED type returns ok=false, which makes assertBindingKind
	// a no-op — a new node type that calls a provider would silently skip the
	// chat-vs-agent posture guard entirely.
	case workflow.NodeTypeLLMCall, workflow.NodeTypeInteractiveLLM, workflow.NodeTypeSpawnWorkflow:
		return provider.ProfileKindChat, true
	case workflow.NodeTypeAgentCall, workflow.NodeTypeInteractiveAgent:
		return provider.ProfileKindAgent, true
	default:
		return "", false
	}
}
```

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/kind_guard.go domain/engine/kind_guard_spawn_test.go
go test ./domain/engine/ -run 'TestExpectedKindFor|TestAssertBindingKind' -v
```

Expected output:

```
--- PASS: TestExpectedKindForSpawnWorkflow (0.00s)
--- PASS: TestAssertBindingKind_SpawnWithAgentBinding_FailsClosed (0.00s)
--- PASS: TestAssertBindingKind_SpawnWithChatBinding_OK (0.00s)
--- PASS: TestExpectedKindForNodeType_OtherMappingsUnchanged (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.4s
```

- [ ] **Step 5: Confirm the engine package still passes**

```bash
go test ./domain/engine/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/engine`

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/kind_guard.go domain/engine/kind_guard_spawn_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "fix(engine): map spawn_workflow to the chat profile kind so the posture guard engages"
```
