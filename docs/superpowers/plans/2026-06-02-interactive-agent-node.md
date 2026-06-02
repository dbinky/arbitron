# Interactive Agent Node + Profile-Aware Interactive Execution — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `interactive_agent` node type (human-in-the-loop session on a tool-enabled, YOLO `claude -p` that writes artifacts to the workspace), rename `interactive` → `interactive_llm`, and make the interactive executor profile-aware — eliminating the large-document output-cap failure that strands brainstorms in `waiting`.

**Architecture:** The interactive executor becomes profile-aware (resolves `model_profile` → binding, like `llm_call`/`agent_call`) and selects a *streaming* backend by the binding's capability **kind**: chat → existing tool-suppressed stream; agent → a new tool-enabled (`--dangerously-skip-permissions`, tools on, `WorkDir={{workspace}}`) stream. Two node types register the same executor logic with that kind-aware stream selector. The completion-sentinel/pause/resume machinery is unchanged; large docs are written via tool calls, so the sentinel payload stays tiny.

**Tech Stack:** Go 1.25, `claude -p --output-format stream-json --resume`, the existing `domain/provider` resolver + `domain/engine` selector ports, `adapter/claudecli`. Spec: `docs/superpowers/specs/2026-06-02-interactive-agent-node-design.md` (read it first).

---

## Conventions & ground rules (read once)

- Strict TDD: for each unit of behavior write the failing test → run it red → minimal code → run it green → commit. Cover the five scenario classes (happy / success / failure / error / edge) where they apply.
- Run tests with `go test ./<pkg>/...`. Run the whole suite with `make test`.
- Hexagonal boundaries: `domain/` imports no adapter; new concrete adapters live under `adapter/`; ports (interfaces) live in `domain/engine` or `domain/provider`.
- Existing patterns to mirror: `domain/engine/executor_llm.go` (profile resolve + selector), `adapter/claudecli/chat_adapter.go` + `agent_adapter.go` (RunSpec/argsPolicy), `adapter/provider/static.go` (resolver), `cmd/arbitron/main.go` (wiring).
- The repo currently has an uncommitted, server-reserialized `workspaces/feature-pipeline/workflow.json`; `git checkout -- workspaces/feature-pipeline/workflow.json` before Task 7 (it is rewritten there anyway).

---

## File Structure

**Engine (domain/engine):**
- `ports.go` — Modify: add `InteractiveStreamSelector` interface (kind-aware streaming provider selection).
- `executor_interactive.go` — Modify: make profile-aware (resolve `model_profile`, select stream backend by kind); take resolver + stream selector instead of a single `ChatProvider`.
- `conversation.go` — Modify: the resumed-turn path (`HandleMessage`) must use the same kind-aware stream selection for the node it resumes.
- `engine.go`, `lint.go` consumers — Modify: rename references.

**Workflow (domain/workflow):**
- `workflow.go` — Modify: rename `NodeTypeInteractive`→`NodeTypeInteractiveLLM` (value `"interactive_llm"`); add `NodeTypeInteractiveAgent` (value `"interactive_agent"`).
- `validation.go` — Modify: kind-check both interactive types in `validateModelProfiles`.
- `lint.go` — Modify: `INTERACTIVE_IN_LOOP` covers both interactive types.

**Adapter (adapter/claudecli):**
- `stream_agent_adapter.go` — Create: `StreamAgentAdapter` implementing `engine.ChatProvider` with tools-on + skip-permissions streaming (the tool-enabled interactive backend).

**Adapter (adapter/provider or adapter/claudecli wiring):**
- `interactive_stream_selector.go` (in `adapter/provider`) — Create: implements `engine.InteractiveStreamSelector`, returning the tool-suppressed chat stream for chat bindings and the `StreamAgentAdapter` for claude-cli agent bindings.

**Wiring & HTTP:**
- `cmd/arbitron/main.go` — Modify: rename; register `interactive_llm` + `interactive_agent`; construct the stream selector; pass resolver+selector to the interactive executor and conversation handler.
- `adapter/http/instance_handler.go` — Modify: rename the `NodeTypeInteractive` check (accept both interactive types where it gates interactive behavior).

**Workflow content (via MCP, Task 7):**
- `workspaces/feature-pipeline/workflow.json`, `templates/brainstorm-*.md`, `orchestration/brainstorm-*.orch.md` — Modify.
- delete `scripts/write-spec.sh`, `scripts/write-designs.sh`.

---

## Task 0: Spike — de-risk tool-enabled streaming + resume

**Goal:** Prove that `claude -p` can stream with tools enabled, write a file, and resume across turns headlessly — before building on it. This is a throwaway verification, not committed code.

- [ ] **Step 1: Run a one-shot tool-enabled stream and confirm a file write.**

```bash
cd /tmp && rm -rf claude-spike && mkdir claude-spike && cd claude-spike
printf 'Write a file called hello.txt containing the word "ok", then say DONE.' | \
  claude -p --verbose --output-format stream-json --dangerously-skip-permissions
ls -la hello.txt && cat hello.txt
```
Expected: `stream-json` lines include `tool_use` (Write) and a final result event; `hello.txt` exists containing `ok`.

- [ ] **Step 2: Capture the session id from the stream, then resume with a second turn.**

```bash
# from Step 1 output, note the session_id field, then:
printf 'Append the word "world" to hello.txt.' | \
  claude -p --verbose --output-format stream-json --dangerously-skip-permissions --resume <SESSION_ID>
cat hello.txt
```
Expected: resume succeeds, `hello.txt` now contains both writes — confirming cross-turn resume with tools works.

- [ ] **Step 3: Record the finding.** Append a short note to the spec's §8 (risks) stating the spike outcome. If resume with tools proves unreliable, STOP and escalate — the fallback (single-turn-per-resume, continuity via workspace files) changes Task 5.

```bash
cd ~/Repos/arbitron
# (edit the spec §8 to record the spike result)
git add docs/superpowers/specs/2026-06-02-interactive-agent-node-design.md
git commit -m "docs: record interactive-agent streaming/resume spike result"
```

---

## Task 1: Node type rename + new constant

**Files:**
- Modify: `domain/workflow/workflow.go`
- Modify (rename references): `cmd/arbitron/main.go`, `adapter/http/instance_handler.go`, `domain/workflow/validation.go:521`, `domain/workflow/lint.go:28`, `domain/engine/conversation.go:317`, `domain/engine/engine.go:513`
- Test: `domain/workflow/workflow_test.go`

- [ ] **Step 1: Write a failing test for the renamed + new constants.**

In `domain/workflow/workflow_test.go`:
```go
func TestInteractiveNodeTypeConstants(t *testing.T) {
	if NodeTypeInteractiveLLM != "interactive_llm" {
		t.Errorf("NodeTypeInteractiveLLM = %q, want interactive_llm", NodeTypeInteractiveLLM)
	}
	if NodeTypeInteractiveAgent != "interactive_agent" {
		t.Errorf("NodeTypeInteractiveAgent = %q, want interactive_agent", NodeTypeInteractiveAgent)
	}
}
```

- [ ] **Step 2: Run it red.** `go test ./domain/workflow/ -run TestInteractiveNodeTypeConstants` → FAIL (undefined symbols).

- [ ] **Step 3: Rename the constant and add the new one** in `domain/workflow/workflow.go`:
```go
	NodeTypeInteractiveLLM   NodeType = "interactive_llm"   // renamed from NodeTypeInteractive
	NodeTypeInteractiveAgent NodeType = "interactive_agent" // new: tool-enabled interactive
```

- [ ] **Step 4: Update every reference** (the build will fail until all are fixed). Replace `NodeTypeInteractive` with `NodeTypeInteractiveLLM` at: `cmd/arbitron/main.go:421`, `domain/workflow/validation.go:521`, `domain/workflow/lint.go:28`, `domain/engine/conversation.go:317`, `domain/engine/engine.go:513`. For `adapter/http/instance_handler.go:129` and the engine/conversation checks that gate *interactive behavior in general*, make them accept **either** interactive type:
```go
func isInteractive(t workflow.NodeType) bool {
	return t == workflow.NodeTypeInteractiveLLM || t == workflow.NodeTypeInteractiveAgent
}
```
and use `isInteractive(node.Type)` at those sites (engine.go:513 IsWait→needs-interaction notification; conversation.go:317 guard; instance_handler.go:129).

- [ ] **Step 5: Verify no stragglers.**
`grep -rn "NodeTypeInteractive\b" --include=*.go .` → expected: no matches (only `...LLM`/`...Agent`). Then `go build ./...` → succeeds.

- [ ] **Step 6: Run the test green + full build.** `go test ./domain/workflow/ -run TestInteractiveNodeTypeConstants` → PASS.

- [ ] **Step 7: Commit.**
```bash
git add -A && git commit -m "refactor: rename interactive -> interactive_llm; add interactive_agent node type"
```

---

## Task 2: Profile-kind validation + lint for both interactive types

**Files:**
- Modify: `domain/workflow/validation.go` (`validateModelProfiles`, ~line 152–195)
- Modify: `domain/workflow/lint.go`
- Test: `domain/workflow/validation_test.go`, `domain/workflow/lint_test.go`

- [ ] **Step 1: Write failing validation tests (happy + failure each way).** In `validation_test.go`, using the existing test harness `ProfileChecker` stub (search the file for how other profile tests build one):
```go
func TestValidate_InteractiveLLMRequiresChatProfile(t *testing.T) {
	wf := wfWithNode(Node{ID: "n", Type: NodeTypeInteractiveLLM, Config: NodeConfig{ModelProfile: "chatp", TemplateFile: "t.md", OrchestrationFile: "o.md"}})
	errs := Validate(wf, fileOK{}, profiles{"chatp": "chat"}, nil)
	if len(errs) != 0 { t.Fatalf("want no errors, got %v", errs) }
}
func TestValidate_InteractiveAgentRequiresAgentProfile(t *testing.T) {
	wf := wfWithNode(Node{ID: "n", Type: NodeTypeInteractiveAgent, Config: NodeConfig{ModelProfile: "chatp", TemplateFile: "t.md", OrchestrationFile: "o.md"}})
	errs := Validate(wf, fileOK{}, profiles{"chatp": "chat"}, nil)
	if !hasCode(errs, "PROFILE_KIND_MISMATCH") { t.Fatalf("want kind-mismatch, got %v", errs) }
}
func TestValidate_InteractiveLLMRejectsAgentProfile(t *testing.T) {
	wf := wfWithNode(Node{ID: "n", Type: NodeTypeInteractiveLLM, Config: NodeConfig{ModelProfile: "agentp", TemplateFile: "t.md", OrchestrationFile: "o.md"}})
	errs := Validate(wf, fileOK{}, profiles{"agentp": "agent"}, nil)
	if !hasCode(errs, "PROFILE_KIND_MISMATCH") { t.Fatalf("want kind-mismatch, got %v", errs) }
}
```
(Match the existing helper/stub names in the file — `wfWithNode`, `profiles`, `fileOK`, `hasCode` are illustrative; reuse whatever `validation_test.go` already defines. Read it first.)

- [ ] **Step 2: Run red.** `go test ./domain/workflow/ -run TestValidate_Interactive` → FAIL.

- [ ] **Step 3: Extend `validateModelProfiles`** to treat the interactive types like their autonomous siblings. In the per-node loop, add:
```go
	case NodeTypeInteractiveLLM:
		wantKind = "chat"
	case NodeTypeInteractiveAgent:
		wantKind = "agent"
```
alongside the existing `llm_call`/`agent_call` cases (the function already requires a non-empty `model_profile`, looks up `ProfileKind`, emits `UNKNOWN_PROFILE` for unknown, and a kind-mismatch error otherwise — interactive types now flow through the same logic). Use the same error code the existing kind-mismatch path uses (read the function to confirm — replace `PROFILE_KIND_MISMATCH` in the tests with the real code).

- [ ] **Step 4: Run green.** `go test ./domain/workflow/ -run TestValidate_Interactive` → PASS.

- [ ] **Step 5: Update lint for both interactive types (edge: interactive in a loop).** In `lint.go`, change the `n.Type != NodeTypeInteractive` guard to skip *unless* it's either interactive type:
```go
	if n.Type != NodeTypeInteractiveLLM && n.Type != NodeTypeInteractiveAgent {
		continue
	}
```
Add/extend `lint_test.go` to assert an `interactive_agent` node on a cycle yields the advisory `INTERACTIVE_IN_LOOP` (warning severity, non-gating). Run `go test ./domain/workflow/` → PASS.

- [ ] **Step 6: Commit.**
```bash
git add -A && git commit -m "feat(validation): kind-check both interactive node types; lint covers interactive_agent in loops"
```

---

## Task 3: Tool-enabled streaming agent adapter

**Files:**
- Create: `adapter/claudecli/stream_agent_adapter.go`
- Test: `adapter/claudecli/stream_agent_adapter_test.go`

The existing `ChatAdapter.StreamChat` (chat_adapter.go) builds args with `argsPolicy{SuppressTools:true}`. The new adapter is identical except its args policy enables tools + skip-permissions (mirroring `agent_adapter.go`'s `spec`). It implements `engine.ChatProvider` so the interactive executor can call `StreamChat` on it uniformly.

- [ ] **Step 1: Write a failing test using the fake `CommandRunner`.** Mirror `chat_adapter_test.go`'s runner stub. Assert the built args.
```go
func TestStreamAgentAdapter_ArgsEnableToolsAndSkipPermissions(t *testing.T) {
	fake := &fakeRunner{lines: resultOnly("DONE")} // reuse the chat adapter test's fake
	a := NewStreamAgentAdapter(Config{Runner: fake, WorkspaceRoot: "/ws", DefaultModel: "m"})
	ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi", WorkDir: "/ws/i1"})
	if err != nil { t.Fatal(err) }
	for range ch { } // drain
	args := strings.Join(fake.lastSpec.Args, " ")
	if !strings.Contains(args, "--dangerously-skip-permissions") { t.Errorf("missing skip-permissions: %s", args) }
	if strings.Contains(args, `--allowedTools ""`) { t.Errorf("tools should NOT be suppressed: %s", args) }
	if fake.lastSpec.Dir != "/ws/i1" { t.Errorf("WorkDir not passed: %q", fake.lastSpec.Dir) }
}
```

- [ ] **Step 2: Run red.** `go test ./adapter/claudecli/ -run TestStreamAgentAdapter` → FAIL (undefined `NewStreamAgentAdapter`).

- [ ] **Step 3: Implement the adapter** in `stream_agent_adapter.go` — copy `ChatAdapter` structure, change the args policy and drop tool suppression:
```go
package claudecli

import (
	"context"
	"fmt"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

// StreamAgentAdapter implements engine.ChatProvider with a TOOL-ENABLED,
// skip-permissions claude -p stream — the backend for interactive_agent nodes.
// Identical to ChatAdapter except tools are NOT suppressed and
// --dangerously-skip-permissions is emitted (YOLO; see design §4).
type StreamAgentAdapter struct{ core adapterCore }

var _ engine.ChatProvider = (*StreamAgentAdapter)(nil)

func NewStreamAgentAdapter(cfg Config) *StreamAgentAdapter {
	return &StreamAgentAdapter{core: newAdapterCore(cfg)}
}

func (a *StreamAgentAdapter) spec(req engine.ChatRequest) RunSpec {
	scope := a.core.allowedTools // full agent scope by default
	return RunSpec{
		Bin: a.core.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:    a.core.defaultModel,
			SkipPermissions: true,
			AllowedTools:    scope,
		}),
		Stdin:            req.Prompt,
		Dir:              req.WorkDir,
		Env:              procenv.BaseEnv(),
		MaxResponseBytes: a.core.maxResponseBytes,
	}
}

// Chat satisfies ChatProvider; interactive uses StreamChat, but the interface
// requires Chat too. Reuse the shared collectResult path.
func (a *StreamAgentAdapter) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil { return nil, err }
	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout); defer cancel()
	result, _, err := collectResult(runCtx, a.core.runner, a.spec(req))
	if err != nil { return nil, err }
	return &engine.ChatResponse{Output: result.Text, SessionID: result.SessionID, Model: req.Model, TokensIn: result.TokensIn, TokensOut: result.TokensOut}, nil
}

// StreamChat mirrors ChatAdapter.StreamChat exactly (same streamState handling)
// but via this adapter's tool-enabled spec().
func (a *StreamAgentAdapter) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil { return nil, err }
	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)
	lines, wait, err := a.core.runner.Run(runCtx, a.spec(req))
	if err != nil { cancel(); return nil, fmt.Errorf("claudecli: stream agent call: %w", err) }
	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel(); defer close(out)
		sendChunk := makeSendChunk(runCtx, out)
		var st streamState
		for line := range lines { st.Handle(line, sendChunk) }
		st.Finalize(wait(), sendChunk)
	}()
	return out, nil
}
```

- [ ] **Step 4: Run green.** `go test ./adapter/claudecli/ -run TestStreamAgentAdapter` → PASS.

- [ ] **Step 5: Edge test — `streamState` tolerates interleaved `tool_use`/`tool_result` frames while still capturing assistant text + the result.** Read `streamState.Handle` / the stream-json line types first; add a test feeding a sequence `[assistant-text, tool_use, tool_result, assistant-text, result]` and assert the accumulated text + Done chunk are correct (and a sentinel embedded in the final assistant text would survive). If `streamState` drops or errors on tool frames, fix it to ignore non-text frames for text accumulation while still surfacing them as (optional) step chunks. Run `go test ./adapter/claudecli/` → PASS.

- [ ] **Step 6: Commit.**
```bash
git add -A && git commit -m "feat(claudecli): tool-enabled streaming agent adapter for interactive_agent"
```

---

## Task 4: Kind-aware interactive stream selector

**Files:**
- Modify: `domain/engine/ports.go` (add interface)
- Create: `adapter/provider/interactive_stream_selector.go`
- Test: `adapter/provider/interactive_stream_selector_test.go`

- [ ] **Step 1: Add the port** in `domain/engine/ports.go`:
```go
// InteractiveStreamSelector chooses the streaming ChatProvider for an
// interactive node from a resolved binding: chat bindings get the tool-suppressed
// chat stream; agent bindings get the tool-enabled (YOLO) stream.
type InteractiveStreamSelector interface {
	StreamProviderFor(b provider.ResolvedBinding) (ChatProvider, error)
}
```

- [ ] **Step 2: Write a failing test** in `adapter/provider/interactive_stream_selector_test.go`:
```go
func TestInteractiveStreamSelector_ChatBindingUsesSuppressedStream(t *testing.T) {
	sel := NewInteractiveStreamSelector(chatSel, agentStream) // see Step 4 wiring
	p, err := sel.StreamProviderFor(provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli"})
	if err != nil { t.Fatal(err) }
	if _, ok := p.(*claudecli.ChatAdapter); !ok { t.Errorf("chat binding should yield ChatAdapter, got %T", p) }
}
func TestInteractiveStreamSelector_AgentBindingUsesToolEnabledStream(t *testing.T) {
	sel := NewInteractiveStreamSelector(chatSel, agentStream)
	p, err := sel.StreamProviderFor(provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli"})
	if err != nil { t.Fatal(err) }
	if _, ok := p.(*claudecli.StreamAgentAdapter); !ok { t.Errorf("agent binding should yield StreamAgentAdapter, got %T", p) }
}
```

- [ ] **Step 3: Run red.** `go test ./adapter/provider/ -run TestInteractiveStreamSelector` → FAIL.

- [ ] **Step 4: Implement the selector.** It delegates chat bindings to the existing `engine.ChatProviderSelector` and returns the tool-enabled stream adapter for agent bindings on the claude-cli provider:
```go
package provider

import (
	"fmt"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

type InteractiveStreamSelector struct {
	chat        engine.ChatProviderSelector
	agentStream engine.ChatProvider // claude-cli tool-enabled streaming adapter
}

func NewInteractiveStreamSelector(chat engine.ChatProviderSelector, agentStream engine.ChatProvider) *InteractiveStreamSelector {
	return &InteractiveStreamSelector{chat: chat, agentStream: agentStream}
}

func (s *InteractiveStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	switch b.Kind {
	case provider.ProfileKindChat:
		return s.chat.ChatProviderFor(b)
	case provider.ProfileKindAgent:
		// MVP: claude-cli only (opencode interactive-agent is a non-goal).
		if b.Provider != "claude-cli" && b.Provider != "" {
			return nil, fmt.Errorf("interactive_agent: provider %q not supported (claude-cli only in MVP)", b.Provider)
		}
		return s.agentStream, nil
	default:
		return nil, fmt.Errorf("interactive stream: unknown profile kind %q", b.Kind)
	}
}
```
(Confirm `provider.ProfileKindChat/Agent` constant names and `ResolvedBinding.Kind/Provider` field names against `domain/provider/*.go` — adjust if they differ.)

- [ ] **Step 5: Run green.** `go test ./adapter/provider/ -run TestInteractiveStreamSelector` → PASS.

- [ ] **Step 6: Commit.**
```bash
git add -A && git commit -m "feat: kind-aware interactive stream selector (chat=suppressed, agent=tool-enabled)"
```

---

## Task 5: Make the interactive executor + conversation handler profile-aware

**Files:**
- Modify: `domain/engine/executor_interactive.go`
- Modify: `domain/engine/conversation.go`
- Test: `domain/engine/executor_interactive_test.go`, `domain/engine/conversation_test.go`

- [ ] **Step 1: Write a failing test** that the executor resolves the profile and uses the selected stream provider, passing `binding.Model` and `WorkDir`. Mirror the existing `executor_interactive_test.go` setup (fake repos/eventbus/queue), but inject a fake `InteractiveStreamSelector` + fake `ProfileResolver`:
```go
func TestInteractiveExecutor_ResolvesProfileAndUsesSelectedStream(t *testing.T) {
	resolver := fakeResolver{"agentp": provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "m1", Provider: "claude-cli"}}
	streamed := &recordingStreamProvider{} // records the ChatRequest it got; returns a one-chunk Done stream
	sel := fakeStreamSelector{want: streamed}
	ex := NewInteractiveExecutor(instRepo, nodeExecRepo, sel, resolver, fileReader, bus, queueMgr)
	node := workflow.Node{ID: "b", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "agentp", OrchestrationFile: "o.md", TemplateFile: "t.md"}}
	_, err := ex.Execute(ctxWithInstance, node, varsWithWorkspace("/ws/i1"), "wf")
	if err != nil { t.Fatal(err) }
	if streamed.lastReq.Model != "m1" { t.Errorf("model from binding not used: %q", streamed.lastReq.Model) }
	if streamed.lastReq.WorkDir != "/ws/i1" { t.Errorf("workdir not passed: %q", streamed.lastReq.WorkDir) }
}
```

- [ ] **Step 2: Run red.** `go test ./domain/engine/ -run TestInteractiveExecutor_ResolvesProfile` → FAIL (constructor signature differs).

- [ ] **Step 3: Refactor `InteractiveExecutor`.** Replace the single `llmProvider ChatProvider` field with `selector InteractiveStreamSelector` + `resolver provider.ProfileResolver`. In `Execute`, after reading orchestration+template and resolving vars, add (mirroring `executor_llm.go:53-63`):
```go
	binding, err := e.resolver.Resolve(ctx, node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: resolve profile: %w", node.ID, err)
	}
	streamProvider, err := e.selector.StreamProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: select stream provider: %w", node.ID, err)
	}
```
then build the `ChatRequest` with `Model: binding.Model` (instead of `node.Config.Model`) and call `streamProvider.StreamChat(ctx, req)`. Keep everything else (sentinel append, conversation persistence, waiting transition) identical. Update `NewInteractiveExecutor`'s signature accordingly.

- [ ] **Step 4: Run green.** `go test ./domain/engine/ -run TestInteractiveExecutor_ResolvesProfile` → PASS.

- [ ] **Step 5: Apply the same profile-aware selection to the resumed-turn path.** `conversation.go`'s `HandleMessage` re-invokes the model for a node mid-session; it must resolve that node's profile and select the same stream provider (so an `interactive_agent` resume is *also* tool-enabled). Add resolver+selector to `conversationHandler`, mirror the resolve+select lines, use `binding.Model`. Write a `conversation_test.go` test: a resumed turn on an `interactive_agent` node uses the agent stream (failure scenario: an unknown profile on resume surfaces an error; success: chat node still uses suppressed stream).

- [ ] **Step 6: Run the engine suite.** `go test ./domain/engine/` → PASS (existing interactive tests adjusted to the new constructor — update their wiring to pass a fake selector+resolver returning a chat binding so behavior is unchanged: the *renamed* `interactive_llm` parity case).

- [ ] **Step 7: Commit.**
```bash
git add -A && git commit -m "feat(engine): profile-aware interactive executor + resumed-turn stream selection"
```

---

## Task 6: Wire everything in main.go

**Files:**
- Modify: `cmd/arbitron/main.go`
- Test: `go build ./...` + targeted run

- [ ] **Step 1: Construct the tool-enabled stream adapter and the interactive stream selector** near the existing chat/agent provider wiring (after the `chatProvider`/`providerFactory` block, ~line 237):
```go
	// Tool-enabled streaming backend for interactive_agent (claude-cli, YOLO).
	streamAgent := claudecli.NewStreamAgentAdapter(agentCfg) // agentCfg already has tools/workspace/timeout
	interactiveStreamSel := providersadapter.NewInteractiveStreamSelector(providerFactory, streamAgent)
```
(`providerFactory` is the existing `engine.ChatProviderSelector`; confirm its variable name in main.go.)

- [ ] **Step 2: Register BOTH interactive executors** (replace the single line 421 registration):
```go
	interactiveExec := engine.NewInteractiveExecutor(
		instanceRepo, nodeExecRepo, interactiveStreamSel, profileResolver, fileReader, bus, queueMgr,
	)
	executors[workflow.NodeTypeInteractiveLLM] = interactiveExec
	executors[workflow.NodeTypeInteractiveAgent] = interactiveExec
```
(One executor instance serves both types; it selects the backend per the node's resolved binding kind.)

- [ ] **Step 3: Update the conversation handler construction** (line ~427) to pass the selector + resolver:
```go
	conversationHandler := engine.NewConversationHandler(
		instanceRepo, nodeExecRepo, interactiveStreamSel, profileResolver, bus, queueMgr,
	)
```

- [ ] **Step 4: Build + run the full suite.** `go build ./... && make test` → all PASS. Fix any remaining signature mismatches.

- [ ] **Step 5: Manual smoke — server boots and validates a tiny interactive_agent workflow.** Rebuild (`make build`), start the server, and `workflow_validate` a 1-node `interactive_agent` workflow referencing an agent profile → expect valid; referencing a chat profile → expect a kind-mismatch error. (Confirms the end-to-end validation path.)

- [ ] **Step 6: Commit.**
```bash
git add -A && git commit -m "feat: register interactive_llm + interactive_agent executors; wire stream selector"
```

---

## Task 7: Update the feature-pipeline workflow (via MCP)

**Files (via MCP tools against the running server):**
- Modify: `workspaces/feature-pipeline/workflow.json` (nodes/edges)
- Modify: `templates/brainstorm-spec.md`, `templates/brainstorm-design.md`, `orchestration/brainstorm-spec.orch.md`, `orchestration/brainstorm-design.orch.md`
- Delete: `scripts/write-spec.sh`, `scripts/write-designs.sh`
- Modify: `~/.arbitron/config.yaml` (profiles)

- [ ] **Step 1: Reset the manifest working copy.** `git checkout -- workspaces/feature-pipeline/workflow.json` (discard the server's re-serialization). Restart the server so it reloads.

- [ ] **Step 2: Add an agent-kind brainstorm profile.** In `~/.arbitron/config.yaml`, change the `interviewer` profile to `kind: agent` (it now drives an interactive_agent), or add `interviewer-agent` (kind agent). Keep `assessor` as chat. Restart the server.

- [ ] **Step 3: Rewrite `orchestration/brainstorm-spec.orch.md`** to instruct an agent (not a chat) — it has tools and a workspace:
```markdown
You are conducting an interactive product-spec brainstorm with a human partner over a chat interface, working in an isolated git worktree. You HAVE tools: read files under ./src to ground yourself, and when the spec is approved, write it to disk yourself. Ask exactly one question per turn and wait. Do not write the spec until the human approves it. When approved: write the full spec to the path given in the template, `git add` + `git commit` it in ./work, then emit the completion result containing ONLY {slug, spec_path} — never the spec body.
```

- [ ] **Step 4: Rewrite `templates/brainstorm-spec.md`** — same brainstorm process as before, but the Output section becomes:
```markdown
## Output (you write the file yourself)
On approval: write the complete spec (Markdown) to `./work/docs/specs/{{run_date}}-{{slug}}-spec.md`, then
`git -C ./work add docs/specs && git -C ./work commit -m "docs: add {{slug}} product spec"`.
Then emit the completion result with ONLY `{slug, spec_path}` (the path you wrote). Do NOT put the spec text in the result.
```
(Keep the one-question-at-a-time process, ./src grounding, YAGNI, strict-TDD-at-design-time guidance.)

- [ ] **Step 5: Same for design** — `orchestration/brainstorm-design.orch.md` + `templates/brainstorm-design.md`: agent writes each `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-{N}.md`, commits, and emits ONLY `{slug, design_paths}`. The multi-phase JSON blob is gone.

- [ ] **Step 6: Update the manifest** via `workflow_update` (workflow_get → modify → update):
  - Change `brainstorm-spec` and `brainstorm-design` `type` to `interactive_agent`, `model_profile` to the agent brainstorm profile. Update their `outputSchema` to the compact shapes: spec `{slug, spec_path}`; design `{slug, design_paths: {type: array, items: string}}`.
  - **Remove** nodes `write-spec` and `write-designs`, and their edges. New edges: `brainstorm-spec → repromote-slug → brainstorm-design → spec-only?`.
  - Keep `repromote-slug` (now `slug` ← `{{brainstorm-spec.slug}}`).
  - `workflow_write_file` the four template/orchestration files; `workflow_delete_file` the two write-*.sh scripts.

- [ ] **Step 7: Validate.** `workflow_validate {name: feature-pipeline}` → expect `valid:true`, with `brainstorm-*` now kind-checked against the agent profile.

- [ ] **Step 8: Commit.**
```bash
git add -A && git commit -m "feat(feature-pipeline): brainstorm nodes become interactive_agent that write their own artifacts"
```

---

## Task 8: End-to-end re-run

- [ ] **Step 1: Trigger the full pipeline** against `https://github.com/dbinky/arbitron.git`, `slug=teams-notifications`, `spec_only=false` (via `instance_trigger`).
- [ ] **Step 2: Drive the spec brainstorm** to approval; confirm the instance **auto-advances** (no stuck `waiting`), the agent wrote+committed `docs/specs/{date}-teams-notifications-spec.md` in the worktree, and the sentinel carried only `{slug, spec_path}`.
- [ ] **Step 3: Drive the design brainstorm**; confirm multiple `docs/designs/...-phase-N.md` written+committed and the instance advances past `spec-only?` into the automated tail.
- [ ] **Step 4: Observe** align → produce → impl loop → ralph loop progress in the trace; confirm agent nodes edit files (they already run YOLO). Let it run to push/PR or stop once the previously-broken brainstorm→advance path is proven.
- [ ] **Step 5: Record results**; update the design spec §8 with any findings.

---

## Self-Review (against the spec)

**Spec coverage:** §2 rename+new type → Task 1; §2 profile-kind validation → Task 2; §3 factor/profile-aware execution + two-streaming-backends → Tasks 3,4,5; §3 resume → Task 0 spike + Task 5 Step 5; §4 agent YOLO (agent_call already YOLO; interactive_agent adopts it) → Task 3 (tool-enabled adapter) + Task 4 (selector); §5 workflow changes → Task 7; §6 acceptance + §7 testing → distributed across tasks (happy/success/failure/error/edge present in Tasks 2,3,5); §6.7 spike → Task 0; §6.6 end-to-end → Task 8.

**Placeholder scan:** test helper names (`wfWithNode`, `profiles`, `fakeResolver`, etc.) are explicitly flagged as "match the existing harness" rather than invented APIs; the real error code for kind-mismatch and the real `ProfileKind*`/`ResolvedBinding` field names are called out to confirm against source before use. No "TBD"/"add error handling"/"similar to Task N".

**Type consistency:** `InteractiveStreamSelector.StreamProviderFor(binding) → ChatProvider` is used identically in the port (Task 4 Step 1), the adapter (Task 4 Step 4), the executor (Task 5 Step 3), and main.go (Task 6 Step 2). `NewInteractiveExecutor(instRepo, nodeExecRepo, selector, resolver, fileReader, bus, queueMgr)` matches between Task 5 and Task 6. `NewConversationHandler(..., selector, resolver, ...)` matches Task 5 Step 5 and Task 6 Step 3. Compact output schemas (`{slug, spec_path}` / `{slug, design_paths}`) match between Task 7 templates and manifest.
