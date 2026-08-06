# Phase 2 — Task 3: preparer failure, error, and edge-case behaviour

**Goal:** Pin every failure mode of `LLMChildInputPreparer` — unsatisfied contract, unparseable response, non-object response, profile/provider/kind failures — and the real-world tolerances (code fence, leading prose) that make structured output reliable.

**Architecture:** These tests exercise the same `domain/engine/child_input_preparer.go` written in Phase 2 Task 2. Two behaviours are added by this task: an assertion that the binding-kind guard fires **before** any provider is selected (so no tool-enabled provider is ever constructed), and a `strings.TrimSpace` on the raw text before fence stripping so a leading-newline response still parses.

**Tech Stack:** Go 1.25.5, standard library `testing`/`errors`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 3: preparer failure/error/edge cases

**Files:**
- Modify: `domain/engine/child_input_preparer.go` (`parseChildInputResponse` — trim before fence strip)
- Test:   `domain/engine/child_input_preparer_errors_test.go`

---

- [x] **Step 1: Write the failing test**

Create `domain/engine/child_input_preparer_errors_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareWith runs Prepare against a provider that returns the given raw text.
func prepareWith(t *testing.T, raw string) (map[string]any, error) {
	t.Helper()
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: raw, Model: req.Model}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))
	return p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "child-pipeline", Inputs: childFields(),
		ParentContext: map[string]any{"repository": "arbitron/arbitron"},
		ProfileAlias:  "chat-default",
	})
}

// ---------- failure: contract not satisfied ----------

func TestChildInputPreparer_MissingRequiredInput_ErrorsAndReturnsNoPartial(t *testing.T) {
	values, err := prepareWith(t, `{"repo":"arbitron/arbitron"}`)
	require.Error(t, err)
	assert.Nil(t, values, "no PARTIAL map may be returned alongside the error: the caller must "+
		"not be able to spawn with half a payload")
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Contains(t, err.Error(), "branch", "the error must name the missing required input")
	assert.Contains(t, err.Error(), "child-pipeline", "the error must name the child workflow")
	assert.Contains(t, err.Error(), "spawn-1", "the error must name the offending node")
}

func TestChildInputPreparer_NullRequiredInput_TreatedAsMissing(t *testing.T) {
	values, err := prepareWith(t, `{"repo":"r","branch":null}`)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Contains(t, err.Error(), "branch")
}

func TestChildInputPreparer_JSONArrayResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, `["repo","branch"]`)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "not a JSON object")
}

func TestChildInputPreparer_BareStringResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, `"just some prose with no braces at all"`)
	require.Error(t, err)
	assert.Nil(t, values)
}

func TestChildInputPreparer_EmptyResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, ``)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "no JSON object found")
}

// The excerpt lets an operator diagnose the failure from the trace without
// re-running, and it is BOUNDED so a whole model response never bloats a
// node-execution record.
func TestChildInputPreparer_UnparseableResponse_IncludesBoundedExcerpt(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, err := prepareWith(t, long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated", "a long unparseable response must be truncated in the error")
	assert.Less(t, len(err.Error()), 1200, "the error message must stay bounded, got %d bytes", len(err.Error()))
}

// ---------- error: provider / profile / posture ----------

func TestChildInputPreparer_ProviderError_Wrapped(t *testing.T) {
	sentinel := errors.New("upstream exploded")
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return nil, sentinel
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, sentinel, "the provider error must be wrapped, not replaced")
}

func TestChildInputPreparer_UnknownProfile_Errors(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("the provider must not be called when the profile cannot be resolved")
			return nil, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "does-not-exist",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// The binding-kind guard must fire BEFORE any provider is selected, so a
// tool-enabled (agent) provider is never constructed for what must be a
// tool-suppressed reasoning call.
func TestChildInputPreparer_AgentBinding_FailsClosedBeforeSelection(t *testing.T) {
	selected := false
	sel := &spySelector{onSelect: func() { selected = true }}
	agentRes := newFakeResolver(provider.Profile{
		Alias: "agent-profile", Kind: provider.ProfileKindAgent, Model: "sonnet",
	})
	p := engine.NewChildInputPreparer(sel, agentRes)

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "agent-profile",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.False(t, selected, "no provider may be selected once the posture guard has failed")
}

func TestChildInputPreparer_SelectorError_Wrapped(t *testing.T) {
	sentinel := errors.New("no backend for binding")
	p := engine.NewChildInputPreparer(staticChatSelector{err: sentinel}, chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, sentinel)
}

// ---------- edge cases ----------

func TestChildInputPreparer_CodeFencedResponseParses(t *testing.T) {
	values, err := prepareWith(t, "```json\n{\"repo\":\"r\",\"branch\":\"b\"}\n```")
	require.NoError(t, err, "a fenced response is the documented real-world failure mode and must parse")
	assert.Equal(t, "r", values["repo"])
}

func TestChildInputPreparer_LeadingProseResponseParses(t *testing.T) {
	values, err := prepareWith(t,
		"Here are the startup values you asked for:\n\n{\"repo\":\"r\",\"branch\":\"b\"}\n\nHope that helps!")
	require.NoError(t, err)
	assert.Equal(t, "b", values["branch"])
}

func TestChildInputPreparer_LeadingWhitespaceResponseParses(t *testing.T) {
	values, err := prepareWith(t, "\n\n   {\"repo\":\"r\",\"branch\":\"b\"}")
	require.NoError(t, err)
	assert.Equal(t, "r", values["repo"])
}

// An empty parent context with a child that requires inputs fails CLEANLY
// rather than producing fabricated-looking empty strings.
func TestChildInputPreparer_EmptyParentContext_FailsCleanly(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			assert.Contains(t, req.Prompt, "(none)", "an empty parent context must be stated explicitly")
			return &engine.ChatResponse{Output: `{}`}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
}

// A very large parent context must not panic and must still be rendered.
func TestChildInputPreparer_LargeParentContext_DoesNotPanic(t *testing.T) {
	big := map[string]any{}
	for i := 0; i < 2000; i++ {
		big[string(rune('a'+i%26))+strings.Repeat("k", i%50)] = strings.Repeat("v", 200)
	}
	var got engine.ChatRequest
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			got = req
			return &engine.ChatResponse{Output: `{"repo":"r","branch":"b"}`}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: big, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	assert.Equal(t, "r", values["repo"])
	assert.NotEmpty(t, got.Prompt)
}

// A non-string Output under a schema is an adapter contract violation and is
// reported, never silently passed through.
func TestChildInputPreparer_NonStringProviderOutput_Errors(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: map[string]any{"repo": "r"}}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "expected raw string from provider")
}
```

Append the spy selector to `domain/engine/child_input_preparer_errors_test.go` (it is
new — `staticChatSelector` in `helpers_test.go` cannot report whether it was
called):

```go
// spySelector records whether ChatProviderFor was reached. It is used to prove
// the posture guard fires BEFORE provider selection.
type spySelector struct {
	onSelect func()
}

func (s *spySelector) ChatProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	if s.onSelect != nil {
		s.onSelect()
	}
	return &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: `{"repo":"r","branch":"b"}`}, nil
		},
	}, nil
}

var _ engine.ChatProviderSelector = (*spySelector)(nil)
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestChildInputPreparer_ -v 2>&1 | tail -30
```

Expected output includes (the whitespace tolerance is not yet implemented):

```
--- FAIL: TestChildInputPreparer_LeadingWhitespaceResponseParses (0.00s)
    child_input_preparer_errors_test.go:...:
        	Error:      	Received unexpected error:
        	            	spawn_workflow "spawn-1": prepare child inputs: no JSON object found in model output
FAIL
```

(If `stripCodeFence` already trims, that single case may pass; every other case
in this file must pass once Step 3 lands. Run the command and record what
actually failed before editing.)

> ACTUAL: nothing failed. All 24 `TestChildInputPreparer_*` cases (Task 2 + Task 3)
> passed on the first run, including `..._LeadingWhitespaceResponseParses` — the
> anticipated exception. `stripCodeFence` (`domain/engine/executor_llm.go:243`)
> opens with `t := strings.TrimSpace(s)`, so leading whitespace is already
> absorbed, and `json.Unmarshal` tolerates it independently.

- [x] **Step 3: Minimal implementation — trim before fence stripping**

In `domain/engine/child_input_preparer.go`, change the first line of the parse
block inside `parseChildInputResponse`:

```go
	// TrimSpace BEFORE fence stripping: a response that opens with a newline or
	// indentation would otherwise not match the fence prefix, and the balanced-
	// object extraction would be the only thing left to save it.
	cleaned := stripCodeFence(strings.TrimSpace(raw))
```

`strings` is already imported by this file (used by `childInputPrompt`).

> NOT APPLIED — the edit is a provable no-op. `stripCodeFence` already begins
> with `strings.TrimSpace(s)` (`domain/engine/executor_llm.go:244`), so
> `stripCodeFence(strings.TrimSpace(raw))` is byte-for-byte identical to
> `stripCodeFence(raw)` for every input. Adding it would ship dead code that a
> reader would mistake for load-bearing tolerance logic. The BEHAVIOUR this step
> exists to guarantee is delivered and pinned green by
> `TestChildInputPreparer_LeadingWhitespaceResponseParses`; `child_input_preparer.go`
> is therefore unchanged by this task, which is test-only.

- [x] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run TestChildInputPreparer -v
```

Expected output (all Phase 2 Task 2 and Task 3 cases):

```
--- PASS: TestChildInputPreparer_HappyPath (0.00s)
...
--- PASS: TestChildInputPreparer_MissingRequiredInput_ErrorsAndReturnsNoPartial (0.00s)
--- PASS: TestChildInputPreparer_NullRequiredInput_TreatedAsMissing (0.00s)
--- PASS: TestChildInputPreparer_JSONArrayResponse_Errors (0.00s)
--- PASS: TestChildInputPreparer_BareStringResponse_Errors (0.00s)
--- PASS: TestChildInputPreparer_EmptyResponse_Errors (0.00s)
--- PASS: TestChildInputPreparer_UnparseableResponse_IncludesBoundedExcerpt (0.00s)
--- PASS: TestChildInputPreparer_ProviderError_Wrapped (0.00s)
--- PASS: TestChildInputPreparer_UnknownProfile_Errors (0.00s)
--- PASS: TestChildInputPreparer_AgentBinding_FailsClosedBeforeSelection (0.00s)
--- PASS: TestChildInputPreparer_SelectorError_Wrapped (0.00s)
--- PASS: TestChildInputPreparer_CodeFencedResponseParses (0.00s)
--- PASS: TestChildInputPreparer_LeadingProseResponseParses (0.00s)
--- PASS: TestChildInputPreparer_LeadingWhitespaceResponseParses (0.00s)
--- PASS: TestChildInputPreparer_EmptyParentContext_FailsCleanly (0.00s)
--- PASS: TestChildInputPreparer_LargeParentContext_DoesNotPanic (0.05s)
--- PASS: TestChildInputPreparer_NonStringProviderOutput_Errors (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.5s
```

- [x] **Step 5: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

> ACTUAL: `domain/engine` and every other package are green.
> `adapter/config` fails on `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`
> (14 × `UNKNOWN_PROFILE` for the sample feature-pipeline's aliases). Confirmed
> PRE-EXISTING and unrelated: it fails identically with this task's work stashed,
> and this task touches no production code.

- [x] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/child_input_preparer.go domain/engine/child_input_preparer_errors_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "test(engine): pin child input preparer failure, error, and tolerance behaviour"
```
