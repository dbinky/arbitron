# Phase 2 — Task 3: MultiNotifier (fan-out composite)

**Goal:** Implement `MultiNotifier`, an `ExternalNotifier` that fans a notification out to each child notifier in order, logging and swallowing individual errors, always returning `nil`.

**Architecture:** Hexagonal — `adapter/notification/multi_notifier.go` implements the domain `notification.ExternalNotifier` port and holds `[]ExternalNotifier`. The engine keeps its single-port wiring; future channels are an append. One child failing never prevents the others from running.

**Tech Stack:** Go stdlib `context`, `log/slog`. Black-box tests (`package notification_test`) with `testify`.

---

## Context

- New file: `adapter/notification/multi_notifier.go` (package `notification`)
- New test: `adapter/notification/multi_notifier_test.go` (black-box `package notification_test`)
- Port: `domain/notification.ExternalNotifier`.
- Reuses `sampleNotification()` / `discardLogger()` defined in `teams_notifier_test.go` (same `notification_test` package, same directory — no redefinition needed).

## Steps

### 1. Write the failing test

- [ ] Create `adapter/notification/multi_notifier_test.go`:

```go
package notification_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/notification"
)

// stubNotifier records calls and optionally returns an error.
type stubNotifier struct {
	calls int
	err   error
}

func (s *stubNotifier) Notify(_ context.Context, _ *notification.Notification) error {
	s.calls++
	return s.err
}

// Success: every child notifier is invoked exactly once.
func TestMultiNotifier_Notify_InvokesEveryChild(t *testing.T) {
	a := &stubNotifier{}
	b := &stubNotifier{}
	m := adaptnotif.NewMultiNotifier(discardLogger(), a, b)

	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
}

// Error: a child erroring is logged and swallowed; later children still run.
func TestMultiNotifier_Notify_OneChildErrorStillRunsOthers(t *testing.T) {
	a := &stubNotifier{err: errors.New("boom")}
	b := &stubNotifier{}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := adaptnotif.NewMultiNotifier(logger, a, b)

	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
	assert.Contains(t, logBuf.String(), "boom")
}

// Edge-case: no children is a clean no-op.
func TestMultiNotifier_Notify_NoChildren(t *testing.T) {
	m := adaptnotif.NewMultiNotifier(discardLogger())
	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
}
```

### 2. Run — confirm it FAILS (does not compile)

- [ ] Run:

```bash
go test ./adapter/notification/ -run TestMultiNotifier
```

- [ ] Expected: build failure, `undefined: adaptnotif.NewMultiNotifier`.

### 3. Implement the MultiNotifier

- [ ] Create `adapter/notification/multi_notifier.go`:

```go
package notification

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/notification"
)

// Compile-time assertion that *MultiNotifier satisfies the port.
var _ notification.ExternalNotifier = (*MultiNotifier)(nil)

// MultiNotifier fans a notification out to each child ExternalNotifier in
// order. Individual child errors are logged and swallowed so one failing
// channel never prevents the others; Notify always returns nil.
type MultiNotifier struct {
	children []notification.ExternalNotifier
	logger   *slog.Logger
}

// NewMultiNotifier constructs a MultiNotifier over the given children.
func NewMultiNotifier(logger *slog.Logger, children ...notification.ExternalNotifier) *MultiNotifier {
	return &MultiNotifier{children: children, logger: logger}
}

// Notify invokes every child in order, logging and swallowing each error.
func (m *MultiNotifier) Notify(ctx context.Context, n *notification.Notification) error {
	for _, child := range m.children {
		if err := child.Notify(ctx, n); err != nil {
			m.logger.Warn("external notifier child failed", "error", err)
		}
	}
	return nil
}
```

### 4. Run — confirm it PASSES

- [ ] Run:

```bash
go test ./adapter/notification/ -run TestMultiNotifier
```

- [ ] Expected: `ok`.

### 5. Run the full notification package suite

- [ ] Run:

```bash
go test ./adapter/notification/
```

- [ ] Expected: `ok` (LogNotifier, payload, TeamsNotifier, MultiNotifier all pass).

### 6. Commit

- [ ] Run:

```bash
git add adapter/notification/multi_notifier.go adapter/notification/multi_notifier_test.go
git commit -m "feat(notification): fan-out MultiNotifier composite"
```
