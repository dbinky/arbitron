package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
)

// WebhookTimerCanceller cancels pending timeout timers.
type WebhookTimerCanceller interface {
	Cancel(instanceID uuid.UUID)
}

// WebhookQueueNotifier triggers queue evaluation.
type WebhookQueueNotifier interface {
	NotifyCapacityFreed(ctx context.Context)
}

// WebhookHandler handles incoming webhook POST requests.
type WebhookHandler struct {
	registry  *WebhookRegistry
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	scheduler WebhookTimerCanceller
	queueRepo instance.QueueRepository
	queueMgr  WebhookQueueNotifier
}

// NewWebhookHandler creates a new WebhookHandler with its dependencies.
func NewWebhookHandler(
	registry *WebhookRegistry,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	scheduler WebhookTimerCanceller,
	queueRepo instance.QueueRepository,
	queueMgr WebhookQueueNotifier,
) *WebhookHandler {
	return &WebhookHandler{
		registry:  registry,
		instances: instances,
		nodeExecs: nodeExecs,
		scheduler: scheduler,
		queueRepo: queueRepo,
		queueMgr:  queueMgr,
	}
}

// ReceiveWebhook handles POST /api/v1/webhooks/wait/{token}.
func (h *WebhookHandler) ReceiveWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Extract token from URL.
	token := chi.URLParam(r, "token")

	// 2. Lookup in registry.
	entry, ok := h.registry.Lookup(token)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 3. Verify instance state (database check).
	inst, err := h.instances.Get(r.Context(), entry.InstanceID)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if inst.State != instance.StateWaiting && inst.State != instance.StatePaused {
		// Instance is no longer waiting (cancelled, completed, failed).
		h.registry.Deactivate(token)
		http.Error(w, "Gone", http.StatusGone)
		return
	}

	// 4. Validate secret if configured.
	if entry.SecretHash != nil {
		secret := r.Header.Get("X-Webhook-Secret")
		if secret == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Recompute HMAC: HMAC-SHA256(key=token, message=secret).
		mac := hmac.New(sha256.New, []byte(token))
		mac.Write([]byte(secret))
		computedHash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(computedHash), []byte(*entry.SecretHash)) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// 5. Read and parse request body as JSON.
	var payload map[string]any
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		payload = map[string]any{}
	} else {
		if err := json.Unmarshal(body, &payload); err != nil {
			payload = map[string]any{} // Non-JSON body -> empty object.
		}
	}

	// 6. Store payload as node output (update existing node execution record).
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(r.Context(), entry.InstanceID, entry.NodeID)
	if err == nil && nodeExec != nil {
		nodeExec.Output = payload
		now := time.Now()
		nodeExec.CompletedAt = &now
		if err := h.nodeExecs.Update(r.Context(), nodeExec); err != nil {
			slog.Error("webhook handler: failed to update node execution output", "instanceID", entry.InstanceID, "error", err)
		}
	}

	// 7. Deactivate token in registry.
	h.registry.Deactivate(token)

	// 8. Cancel pending timeout timer if configured.
	h.scheduler.Cancel(entry.InstanceID)

	// 9. Set webhook_received_at on instance.
	now := time.Now()
	if err := h.instances.SetWebhookReceived(r.Context(), entry.InstanceID, now); err != nil {
		slog.Error("webhook handler: failed to set webhook received timestamp", "instanceID", entry.InstanceID, "error", err)
	}

	// 10. Handle state transition.
	if inst.State == instance.StateWaiting {
		// Transition to queued.
		if err := h.queueRepo.SetQueued(r.Context(), entry.InstanceID, now); err != nil {
			slog.Error("webhook handler: failed to transition instance to queued after webhook received", "instanceID", entry.InstanceID, "error", err)
		} else {
			h.queueMgr.NotifyCapacityFreed(r.Context())
		}
	}
	// If paused: payload stored, endpoint deactivated. Resume logic handles transition.

	// 11. Return 200 OK.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}
