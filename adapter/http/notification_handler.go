package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/notification"
)

// NotificationHandler handles REST API requests for notification operations.
type NotificationHandler struct {
	repo     notification.Repository
	eventBus engine.EventBus
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(repo notification.Repository) *NotificationHandler {
	return &NotificationHandler{repo: repo}
}

// SetEventBus wires the event bus so that mark-read and dismiss actions
// broadcast WS events for cross-tab synchronisation.
func (h *NotificationHandler) SetEventBus(bus engine.EventBus) {
	h.eventBus = bus
}

// notificationResponse is the JSON representation of a notification.
type notificationResponse struct {
	ID           string `json:"id"`
	InstanceID   string `json:"instanceId"`
	WorkflowName string `json:"workflowName"`
	Type         string `json:"type"`
	Read         bool   `json:"read"`
	Dismissed    bool   `json:"dismissed"`
	CreatedAt    string `json:"createdAt"`
}

// notificationListResponse wraps a page of notifications with a hasMore sentinel.
type notificationListResponse struct {
	Items   []notificationResponse `json:"items"`
	HasMore bool                   `json:"hasMore"`
}

// notifOwnerID returns the owner restriction for the calling identity:
// nil for admins (see all), &userID for non-admins (see own only).
// When no identity is in context (local mode) nil is returned so local mode
// remains frictionless and every notification is visible.
func notifOwnerID(r *http.Request) *uuid.UUID {
	id, ok := identity.FromContext(r.Context())
	if !ok || id.IsAdmin() {
		return nil
	}
	uid := id.UserID
	return &uid
}

// List handles GET /api/v1/notifications.
func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	userLimit := 20
	filters := notification.ListFilters{
		Offset:  0,
		OwnerID: notifOwnerID(r),
	}

	if v := r.URL.Query().Get("dismissed"); v != "" {
		d, err := strconv.ParseBool(v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid dismissed parameter")
			return
		}
		filters.Dismissed = &d
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil || l < 1 {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid limit parameter")
			return
		}
		if l > 100 {
			l = 100
		}
		userLimit = l
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		o, err := strconv.Atoi(v)
		if err != nil || o < 0 {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid offset parameter")
			return
		}
		filters.Offset = o
	}

	// Request one extra item to determine whether another page exists.
	filters.Limit = userLimit + 1

	notifications, err := h.repo.List(r.Context(), filters)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list notifications")
		return
	}

	hasMore := len(notifications) > userLimit
	if hasMore {
		notifications = notifications[:userLimit]
	}

	items := make([]notificationResponse, len(notifications))
	for i, n := range notifications {
		items[i] = notificationResponse{
			ID:           n.ID.String(),
			InstanceID:   n.InstanceID.String(),
			WorkflowName: n.WorkflowName,
			Type:         string(n.Type),
			Read:         n.Read,
			Dismissed:    n.Dismissed,
			CreatedAt:    n.CreatedAt.Format(APITimeFormatMillis),
		}
	}

	WriteJSON(w, http.StatusOK, notificationListResponse{Items: items, HasMore: hasMore})
}

// UnreadCount handles GET /api/v1/notifications/unread-count.
func (h *NotificationHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	count, err := h.repo.CountUnread(r.Context(), notifOwnerID(r))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to count unread notifications")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]int{"count": count})
}

// MarkRead handles POST /api/v1/notifications/mark-read.
func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.MarkAllRead(r.Context(), notifOwnerID(r)); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark notifications read")
		return
	}
	if h.eventBus != nil {
		h.eventBus.Publish(uuid.Nil, engine.Event{
			Type:      "notification.read",
			Timestamp: time.Now(),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Dismiss handles POST /api/v1/notifications/{id}/dismiss.
func (h *NotificationHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid notification id")
		return
	}

	if err := h.repo.Dismiss(r.Context(), id, notifOwnerID(r)); err != nil {
		if errors.Is(err, notification.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "notification not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss notification")
		return
	}
	if h.eventBus != nil {
		h.eventBus.Publish(uuid.Nil, engine.Event{
			Type:      "notification.dismissed",
			Timestamp: time.Now(),
			Data:      map[string]any{"id": idStr},
		})
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
