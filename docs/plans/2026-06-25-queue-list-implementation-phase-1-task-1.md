# Phase 1 — Task 1: DeriveRunName helper + EnrichedInstanceSummary type

**Goal:** Add the pure `DeriveRunName` domain helper that computes a concise run name from a workflow name, submitted variables, workflow input metadata, and instance id; the `EnrichedInstanceSummary` additive row type that carries every enriched field the queue list needs; and the `OwnerID` field on `InstanceSummary` (the lean list shape does not carry it today, but the enricher needs it for the batch user lookup — this is the one additive widening of the lean shape, included here so Task 3's enricher can read `s.OwnerID`).

**Architecture:** Hexagonal — all three live in `domain/instance` (the dependency floor) with zero adapter imports. `DeriveRunName` is a pure function implementing the name ladder (title → name → slug → first-required input → first non-empty input → short id) so it never errors. `EnrichedInstanceSummary` embeds `InstanceSummary` so the lean shape and its existing consumers are untouched. `OwnerID` is added to `InstanceSummary` so the enricher (Task 3) can batch-resolve submitters without a per-row instance load for the owner; the SQLite List query + mapper are updated in Task 4 (which switches the handler) to populate it. No I/O, no wiring in this task — the enrichment step that calls `DeriveRunName` is Task 3; the composition-root wiring is Task 5.

**Tech Stack:** Go, standard library only. Tests with `testing` + `testify` (already in `go.sum`).

---

### Task 1: DeriveRunName + EnrichedInstanceSummary

**Files:**
- Create: `domain/instance/runname.go`
- Create: `domain/instance/runname_test.go`
- Modify: `domain/instance/instance.go` (append `EnrichedInstanceSummary` after `InstanceSummary`)

- [x] Step 1: Write the failing test for `DeriveRunName` (all ladder rungs + edge cases)

Create `domain/instance/runname_test.go`:

```go
package instance_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestDeriveRunName(t *testing.T) {
	wfName := "feature-pipeline"
	id := uuid.MustParse("12345678-1234-5678-1234-567812345678")

	t.Run("title variable wins", func(t *testing.T) {
		vars := map[string]any{"title": "Refactor checkout flow"}
		inputs := []workflow.InputField{{Name: "title", Type: "string"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "Refactor checkout flow — feature-pipeline", got)
	})

	t.Run("name variable when no title", func(t *testing.T) {
		vars := map[string]any{"name": "nightly-backfill"}
		inputs := []workflow.InputField{{Name: "name", Type: "string"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "nightly-backfill — feature-pipeline", got)
	})

	t.Run("slug variable when no title or name", func(t *testing.T) {
		vars := map[string]any{"slug": "user-reports-q2"}
		inputs := []workflow.InputField{{Name: "slug", Type: "string"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "user-reports-q2 — feature-pipeline", got)
	})

	t.Run("first required input when no title/name/slug", func(t *testing.T) {
		vars := map[string]any{"feature": "dark-mode", "region": "eu"}
		inputs := []workflow.InputField{
			{Name: "region", Type: "string", Required: false},
			{Name: "feature", Type: "string", Required: true},
		}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "dark-mode — feature-pipeline", got)
	})

	t.Run("first non-empty input when no required inputs", func(t *testing.T) {
		vars := map[string]any{"region": "eu", "env": "staging"}
		inputs := []workflow.InputField{
			{Name: "region", Type: "string"},
			{Name: "env", Type: "string"},
		}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "eu — feature-pipeline", got)
	})

	t.Run("short id when no usable variables", func(t *testing.T) {
		vars := map[string]any{}
		inputs := []workflow.InputField{}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "feature-pipeline 12345678", got)
	})

	t.Run("short id when variables are non-string", func(t *testing.T) {
		vars := map[string]any{"count": 42, "enabled": true}
		inputs := []workflow.InputField{{Name: "count", Type: "int"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "feature-pipeline 12345678", got)
	})

	t.Run("empty workflow name still yields short id", func(t *testing.T) {
		got := instance.DeriveRunName("", map[string]any{}, nil, id)
		assert.Equal(t, "12345678", got)
	})

	t.Run("very long title is truncated", func(t *testing.T) {
		longTitle := string([]byte{0x61}); for i := 0; i < 200; i++ { longTitle += "a" }
		vars := map[string]any{"title": longTitle}
		inputs := []workflow.InputField{{Name: "title", Type: "string"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.LessOrEqual(t, len(got), 80, "long titles are truncated")
		assert.Contains(t, got, "feature-pipeline")
	})
}
```

- [x] Step 2: Run it, confirm it fails

```bash
go test ./domain/instance/
```

Expected: build failure — `undefined: instance.DeriveRunName`.

- [x] Step 3: Minimal implementation of `DeriveRunName`

Create `domain/instance/runname.go`:

```go
package instance

import (
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// maxRunNameLen caps the generated run name so a very long submitted title
// cannot blow out the queue row. The workflow suffix is always preserved.
const maxRunNameLen = 80

// DeriveRunName computes a concise, human-readable run name from the best
// available submitted label plus the workflow name. It is pure and
// deterministic and NEVER errors: the final ladder rung (short id) always
// yields a value, so a run name is always present.
//
// Ladder (first non-empty wins):
//  1. a "title" variable
//  2. a "name" variable
//  3. a "slug" variable
//  4. the first REQUIRED input's submitted value
//  5. the first non-empty submitted input value
//  6. "<workflowName> <short-id>" (or just "<short-id>" when workflowName is "")
//
// Only string-valued variables are usable; non-string values fall through to
// the next rung. The result is truncated to maxRunNameLen.
func DeriveRunName(workflowName string, variables map[string]any, inputs []workflow.InputField, id uuid.UUID) string {
	label := pickRunLabel(variables, inputs)
	label = truncate(label, maxRunNameLen-len(workflowName)-1)

	switch {
	case label != "" && workflowName != "":
		return label + " — " + workflowName
	case label != "":
		return label
	case workflowName != "":
		return workflowName + " " + shortID(id)
	default:
		return shortID(id)
	}
}

// pickRunLabel resolves the best submitted label per the ladder, or "" when
// none of the rungs yield a usable string.
func pickRunLabel(variables map[string]any, inputs []workflow.InputField) string {
	for _, key := range []string{"title", "name", "slug"} {
		if s := stringVar(variables, key); s != "" {
			return s
		}
	}
	for _, inp := range inputs {
		if inp.Required {
			if s := stringVar(variables, inp.Name); s != "" {
				return s
			}
		}
	}
	for _, inp := range inputs {
		if s := stringVar(variables, inp.Name); s != "" {
			return s
		}
	}
	return ""
}

func stringVar(vars map[string]any, key string) string {
	if vars == nil {
		return ""
	}
	v, ok := vars[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// shortID returns the first hyphen-separated group of the UUID (8 hex chars),
// matching the common short-id rendering.
func shortID(id uuid.UUID) string {
	s := id.String()
	if i := strings.Index(s, "-"); i > 0 {
		return s[:i]
	}
	return s
}
```

- [x] Step 4: Run it, confirm it passes

```bash
go test ./domain/instance/
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/instance	0.00s
```

- [x] Step 5: Write the failing test for `EnrichedInstanceSummary` field shape

Append to `domain/instance/runname_test.go`:

```go
func TestEnrichedInstanceSummary_EmbedsSummary(t *testing.T) {
	sum := instance.InstanceSummary{
		ID:           uuid.New(),
		WorkflowName: "wf",
		State:        instance.StateRunning,
		Priority:     instance.PriorityNormal,
	}
	enr := instance.EnrichedInstanceSummary{
		InstanceSummary: sum,
		RunName:         "my-run — wf",
		Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
		FriendlyStep:    "Generate draft",
		Model:           "gpt-4o",
		Backend:         "claude-code",
		RetryCount:      2,
		QueuePosition:   3,
	}
	assert.Equal(t, sum.ID, enr.ID, "embedded summary fields are accessible")
	assert.Equal(t, "my-run — wf", enr.RunName)
	assert.Equal(t, "alice", enr.Submitter.Name)
	assert.Equal(t, "alice@example.com", enr.Submitter.AccountIdentity)
	assert.Equal(t, "Generate draft", enr.FriendlyStep)
	assert.Equal(t, "gpt-4o", enr.Model)
	assert.Equal(t, "claude-code", enr.Backend)
	assert.Equal(t, 2, enr.RetryCount)
	assert.Equal(t, 3, enr.QueuePosition)
}

func TestEnrichedInstanceSummary_ZeroValueDegrades(t *testing.T) {
	var enr instance.EnrichedInstanceSummary
	assert.Equal(t, "", enr.RunName, "missing run name is empty, not nil")
	assert.Equal(t, 0, enr.QueuePosition, "non-queued rows carry position 0")
	assert.Equal(t, "", enr.Submitter.Name)
}
```

- [x] Step 6: Run it, confirm it fails

```bash
go test ./domain/instance/
```

Expected: build failure — `undefined: instance.EnrichedInstanceSummary`, `undefined: instance.Submitter`.

- [x] Step 7: Add `OwnerID` to `InstanceSummary`, and `EnrichedInstanceSummary` + `Submitter` to `instance.go`

First, add `OwnerID` to the existing `InstanceSummary` struct in `domain/instance/instance.go`. Insert the field after `ID` (line 137):

```go
// InstanceSummary is a lightweight representation for listing instances.
type InstanceSummary struct {
	ID            uuid.UUID     `json:"id"`
	OwnerID       uuid.UUID     `json:"ownerId"`
	WorkflowName  string        `json:"workflowName"`
	State         State         `json:"state"`
	Priority      Priority      `json:"priority"`
	Department    string        `json:"department,omitempty"`
	Visibility    Visibility    `json:"visibility,omitempty"`
	TriggerSource TriggerSource `json:"triggerSource"`
	ScheduleID    *uuid.UUID    `json:"scheduleId,omitempty"`
	CurrentNodeID *string       `json:"currentNodeId,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
	CompletedAt   *time.Time    `json:"completedAt,omitempty"`
	QueuedAt      *time.Time    `json:"queuedAt,omitempty"`
}
```

Then, append the new types immediately after the `InstanceSummary` struct (after line 150):

```go
// Submitter is the resolved owner identity for an enriched list row. Name is
// the display name (Username); AccountIdentity is the email or IdP subject,
// surfaced only in shared-mode row details. Both may be empty when the user
// lookup degrades (best-effort enrichment).
type Submitter struct {
	Name            string
	AccountIdentity string
}

// EnrichedInstanceSummary is the additive, per-row projection the richer
// queue table consumes. It embeds InstanceSummary so the lean shape and its
// existing consumers (HTTP summaryDTO, MCP list) are untouched. Every
// enriched field is best-effort: a producer failure leaves the field at its
// zero value and the row still renders.
type EnrichedInstanceSummary struct {
	InstanceSummary
	// RunName is always present (DeriveRunName never errors).
	RunName string
	// Submitter is the resolved owner; Name is the display label.
	Submitter Submitter
	// FriendlyStep is the relevant node's Label (current/failed/last), or "".
	FriendlyStep string
	// Model is the latest LLM model used (from the node-exec aggregate), or "".
	Model string
	// Backend is the relevant node's configured backend, or "" (BackendUnset).
	Backend string
	// RetryCount is the max attempt across the instance's node executions.
	RetryCount int
	// QueuePosition is the 1-based priority-aware position for queued rows; 0
	// for non-queued rows (position is absent, not "first").
	QueuePosition int
	// FailedNodeID is carried from the full instance so the dashboard detail
	// panel can lazy-load the last error via the trace endpoint. Nil for
	// non-failed rows.
	FailedNodeID *string
}
```

- [x] Step 8: Run it, confirm it passes

```bash
go test ./domain/instance/
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/instance	0.00s
```

- [x] Step 9: Confirm the whole module builds

```bash
go build ./...
```

Expected: no output, exit code 0.

- [ ] Step 10: Commit

```bash
git add domain/instance/runname.go domain/instance/runname_test.go domain/instance/instance.go
git commit -m "feat(instance): add DeriveRunName helper + EnrichedInstanceSummary type"
```