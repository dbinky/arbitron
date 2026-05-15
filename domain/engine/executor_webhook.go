package engine

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// maxWebhookTimeout is the upper bound for webhook wait timeout durations.
// Durations exceeding this are rejected to prevent resource exhaustion via
// accumulated never-firing timers.
const maxWebhookTimeout = 7 * 24 * time.Hour

// WebhookWaitExecutor handles webhook_wait nodes by generating a unique token,
// transitioning instances to the waiting state, and registering with the webhook
// registry for incoming POST requests.
type WebhookWaitExecutor struct {
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	registry  WebhookRegistrar
	scheduler TimerScheduler
	queueMgr  QueueManager
}

// NewWebhookWaitExecutor constructs a new WebhookWaitExecutor.
func NewWebhookWaitExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	registry WebhookRegistrar,
	scheduler TimerScheduler,
	queueMgr QueueManager,
) *WebhookWaitExecutor {
	return &WebhookWaitExecutor{
		instances: instances,
		nodeExecs: nodeExecs,
		registry:  registry,
		scheduler: scheduler,
		queueMgr:  queueMgr,
	}
}

// Execute generates a cryptographically random token, hashes the secret if
// configured, transitions the instance to waiting, and registers the token.
func (e *WebhookWaitExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("webhook_wait executor: instance ID not found in context")
	}

	startTime := time.Now()

	// 1. Generate cryptographically random URL-safe token (32 bytes).
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	// 2. Hash secret if configured.
	var secretHash *string
	if node.Config.Secret != "" {
		mac := hmac.New(sha256.New, []byte(token))
		mac.Write([]byte(node.Config.Secret))
		hash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		secretHash = &hash
	}

	// 3. Parse timeout if configured.
	var deadline *time.Time
	if node.Config.WaitTimeoutSeconds != "" {
		timeoutStr := variable.Resolve(node.Config.WaitTimeoutSeconds, vars)
		timeoutSec, err := strconv.ParseFloat(timeoutStr, 64)
		if err != nil {
			return nil, fmt.Errorf("waitTimeoutSeconds is not numeric: %q", timeoutStr)
		}
		if timeoutSec > maxWebhookTimeout.Seconds() {
			return nil, fmt.Errorf("waitTimeoutSeconds exceeds maximum allowed (%s): %v", maxWebhookTimeout, timeoutSec)
		}
		d := time.Now().Add(time.Duration(timeoutSec * float64(time.Second)))
		deadline = &d
	}

	// 4. Write node execution record (instance still running — safe to return error if this fails).
	webhookURL := fmt.Sprintf("/api/v1/webhooks/wait/%s", token)
	now := time.Now()
	exec := &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      map[string]any{"webhookUrl": webhookURL},
		StartedAt:   startTime,
		CompletedAt: &now,
	}
	if err := e.nodeExecs.Create(ctx, exec); err != nil {
		return nil, fmt.Errorf("creating node execution: %w", err)
	}

	// 5. Store webhook fields before transitioning to waiting. This order ensures
	// that if SetWaiting succeeds but SetWebhookFields fails, the instance is not
	// stuck in waiting with no token (ListWaitingWithWebhook filters by non-null token).
	if err := e.instances.SetWebhookFields(ctx, instanceID, &token, secretHash); err != nil {
		return nil, fmt.Errorf("setting webhook fields: %w", err)
	}

	// 6. Transition instance to waiting, committing the capacity release.
	setWaitingErr := func() error {
		if deadline != nil {
			return e.instances.SetWaiting(ctx, instanceID, deadline)
		}
		return e.instances.UpdateState(ctx, instanceID, instance.StateWaiting)
	}()
	if setWaitingErr != nil {
		// Roll back webhook fields so the instance doesn't retain a stale token.
		_ = e.instances.SetWebhookFields(ctx, instanceID, nil, nil)
		return nil, fmt.Errorf("setting waiting state: %w", setWaitingErr)
	}

	// 7. Register with webhook registry.
	e.registry.Register(token, instanceID, node.ID, secretHash)

	// 8. If timeout configured, register with scheduler.
	if deadline != nil {
		e.scheduler.Schedule(instanceID, *deadline, TimerTypeWebhook)
	}

	// 9. Notify queue manager (capacity freed).
	e.queueMgr.NotifyCapacityFreed(ctx)

	// 10. Return wait result.
	return &NodeResult{IsWait: true}, nil
}
