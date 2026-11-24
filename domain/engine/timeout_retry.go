package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Timeout-aware retry. A node attempt whose own timeoutSeconds expired is
// retried with a longer timeout, because an identical retry almost always
// times out identically.
const (
	// defaultTimeoutRetryScale multiplies the timeout after each timed-out
	// attempt when the node does not set timeoutRetryScale.
	defaultTimeoutRetryScale = 2
	// maxTimeoutRetryMultiple caps a scaled timeout at this multiple of the
	// node's configured timeoutSeconds.
	maxTimeoutRetryMultiple = 4
	// maxScaledTimeoutSeconds is an absolute ceiling on a scaled timeout (6h).
	// It never shrinks a timeout below the current attempt's.
	maxScaledTimeoutSeconds = 6 * 60 * 60
)

// honoursNodeTimeout reports whether the node type's executor applies
// Config.TimeoutSeconds as its own deadline. Other types may still fail with
// context.DeadlineExceeded (an inner provider timeout), which is not the
// node's timeout and must not be scaled.
func honoursNodeTimeout(t workflow.NodeType) bool {
	switch t {
	case workflow.NodeTypeScript, workflow.NodeTypeToolCall, workflow.NodeTypeAgentCall:
		return true
	}
	return false
}

// isNodeTimeout reports whether err means the attempt ran out of the node's
// own timeout. The executors derive the node deadline from the attempt
// context, so a deadline error while that parent context is still alive can
// only be the node's own. A done parent context (pause, cancel, shutdown) is
// never a node timeout.
func isNodeTimeout(ctx context.Context, node workflow.Node, err error) bool {
	if err == nil || node.Config.TimeoutSeconds <= 0 || !honoursNodeTimeout(node.Type) {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// nextTimeoutSeconds is the timeout for the attempt after one that timed out
// with current seconds: current x scale, capped at 4x configured and at the
// absolute ceiling (which never shrinks below current). scale 0 means the
// default; scale <= 1 keeps the timeout unchanged.
func nextTimeoutSeconds(current, configured, scale int) int {
	if scale == 0 {
		scale = defaultTimeoutRetryScale
	}
	if scale <= 1 {
		return current
	}
	next := current * scale
	if limit := configured * maxTimeoutRetryMultiple; next > limit {
		next = limit
	}
	if ceiling := max(maxScaledTimeoutSeconds, current); next > ceiling {
		next = ceiling
	}
	if next < current {
		next = current
	}
	return next
}

// nodeTimeoutError explains a timed-out attempt: which timeout expired, which
// attempt it was, and what the next attempt gets. It wraps the original error
// so errors.Is(err, context.DeadlineExceeded) still holds.
type nodeTimeoutError struct {
	node        workflow.Node
	timeoutSecs int
	attempt     int
	maxAttempts int
	nextSecs    int
	err         error
}

func (e *nodeTimeoutError) Error() string {
	next := "no attempts left"
	if e.attempt < e.maxAttempts {
		next = fmt.Sprintf("next attempt %ds", e.nextSecs)
	}
	return fmt.Sprintf("%s %q: timed out after %ds (attempt %d/%d); %s: %v",
		e.node.Type, e.node.ID, e.timeoutSecs, e.attempt, e.maxAttempts, next, e.err)
}

func (e *nodeTimeoutError) Unwrap() error { return e.err }
