package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Mock event bus for notification event tests
// ---------------------------------------------------------------------------

type notifMockEventBus struct {
	mu     sync.Mutex
	events []engine.Event
}

func (b *notifMockEventBus) Publish(_ uuid.UUID, evt engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

func (b *notifMockEventBus) Subscribe(_ uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	return ch, func() { close(ch) }
}

func (b *notifMockEventBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent)
	return ch, func() { close(ch) }
}

func (b *notifMockEventBus) published() []engine.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]engine.Event, len(b.events))
	copy(out, b.events)
	return out
}

// ---------------------------------------------------------------------------
// Mock notification repository
// ---------------------------------------------------------------------------

type mockNotificationRepo struct {
	notifications []notification.Notification
	unreadCount   int
	listErr       error
	countErr      error
	markReadErr   error
	dismissErr    error
	dismissedID   uuid.UUID
}

func newMockNotificationRepo() *mockNotificationRepo {
	return &mockNotificationRepo{}
}

func (r *mockNotificationRepo) Create(_ context.Context, n *notification.Notification) error {
	r.notifications = append(r.notifications, *n)
	return nil
}

func (r *mockNotificationRepo) List(_ context.Context, filters notification.ListFilters) ([]notification.Notification, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	result := make([]notification.Notification, 0, len(r.notifications))
	for _, n := range r.notifications {
		if filters.Dismissed != nil && n.Dismissed != *filters.Dismissed {
			continue
		}
		result = append(result, n)
	}
	offset := filters.Offset
	if offset > len(result) {
		return []notification.Notification{}, nil
	}
	result = result[offset:]
	if filters.Limit > 0 && len(result) > filters.Limit {
		result = result[:filters.Limit]
	}
	return result, nil
}

func (r *mockNotificationRepo) CountUnread(_ context.Context, _ *uuid.UUID) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.unreadCount, nil
}

func (r *mockNotificationRepo) MarkAllRead(_ context.Context, _ *uuid.UUID) error {
	return r.markReadErr
}

func (r *mockNotificationRepo) Dismiss(_ context.Context, id uuid.UUID, _ *uuid.UUID) error {
	if r.dismissErr != nil {
		return r.dismissErr
	}
	r.dismissedID = id
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testNotification(wfName string, notifType notification.Type) notification.Notification {
	return notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.New(),
		WorkflowName: wfName,
		Type:         notifType,
		Read:         false,
		Dismissed:    false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// listResp is a helper to decode the paginated list envelope.
type listResp struct {
	Items   []map[string]any `json:"items"`
	HasMore bool             `json:"hasMore"`
}

// ---------------------------------------------------------------------------
// Tests: List
// ---------------------------------------------------------------------------

func TestNotificationHandler_List(t *testing.T) {
	t.Run("returns empty items and hasMore=false when no notifications", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp.Items)
		assert.False(t, resp.HasMore)
	})

	t.Run("returns notifications with correct fields", func(t *testing.T) {
		repo := newMockNotificationRepo()
		n := testNotification("my-workflow", notification.TypeCompleted)
		repo.notifications = []notification.Notification{n}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, n.ID.String(), resp.Items[0]["id"])
		assert.Equal(t, n.InstanceID.String(), resp.Items[0]["instanceId"])
		assert.Equal(t, "my-workflow", resp.Items[0]["workflowName"])
		assert.Equal(t, "completed", resp.Items[0]["type"])
		assert.Equal(t, false, resp.Items[0]["read"])
		assert.Equal(t, false, resp.Items[0]["dismissed"])
		assert.NotEmpty(t, resp.Items[0]["createdAt"])
		assert.False(t, resp.HasMore)
	})

	t.Run("filters by dismissed=true", func(t *testing.T) {
		repo := newMockNotificationRepo()
		active := testNotification("wf-a", notification.TypeFailed)
		dismissed := testNotification("wf-b", notification.TypeFailed)
		dismissed.Dismissed = true
		repo.notifications = []notification.Notification{active, dismissed}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?dismissed=true", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, dismissed.ID.String(), resp.Items[0]["id"])
	})

	t.Run("respects limit and offset", func(t *testing.T) {
		repo := newMockNotificationRepo()
		for i := 0; i < 5; i++ {
			repo.notifications = append(repo.notifications, testNotification("wf", notification.TypeCompleted))
		}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=2&offset=1", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Len(t, resp.Items, 2)
		assert.True(t, resp.HasMore, "4 items remain after offset=1, limit=2 should leave more")
	})

	t.Run("hasMore is false when results fit within limit", func(t *testing.T) {
		repo := newMockNotificationRepo()
		for i := 0; i < 3; i++ {
			repo.notifications = append(repo.notifications, testNotification("wf", notification.TypeCompleted))
		}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=5", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Len(t, resp.Items, 3)
		assert.False(t, resp.HasMore)
	})

	t.Run("hasMore is true when exactly limit+1 items exist", func(t *testing.T) {
		repo := newMockNotificationRepo()
		for i := 0; i < 6; i++ {
			repo.notifications = append(repo.notifications, testNotification("wf", notification.TypeCompleted))
		}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=5", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Len(t, resp.Items, 5)
		assert.True(t, resp.HasMore)
	})

	t.Run("caps limit at 100", func(t *testing.T) {
		repo := newMockNotificationRepo()
		for i := 0; i < 120; i++ {
			repo.notifications = append(repo.notifications, testNotification("wf", notification.TypeCompleted))
		}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=200", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp listResp
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.LessOrEqual(t, len(resp.Items), 100)
		assert.True(t, resp.HasMore)
	})

	t.Run("returns 400 for invalid dismissed param", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?dismissed=maybe", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 400 for invalid limit param", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=abc", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for negative offset", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?offset=-1", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.listErr = fmt.Errorf("db error")
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: UnreadCount
// ---------------------------------------------------------------------------

func TestNotificationHandler_UnreadCount(t *testing.T) {
	t.Run("returns unread count", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.unreadCount = 7
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil)
		w := httptest.NewRecorder()
		h.UnreadCount(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]int
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, 7, resp["count"])
	})

	t.Run("returns 0 when no unread notifications", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil)
		w := httptest.NewRecorder()
		h.UnreadCount(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]int
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, 0, resp["count"])
	})

	t.Run("returns 500 when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.countErr = fmt.Errorf("db error")
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil)
		w := httptest.NewRecorder()
		h.UnreadCount(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: MarkRead
// ---------------------------------------------------------------------------

func TestNotificationHandler_MarkRead(t *testing.T) {
	t.Run("marks all notifications read and returns ok", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil)
		w := httptest.NewRecorder()
		h.MarkRead(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "ok", resp["status"])
	})

	t.Run("returns 500 when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.markReadErr = fmt.Errorf("db error")
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil)
		w := httptest.NewRecorder()
		h.MarkRead(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Dismiss
// ---------------------------------------------------------------------------

func TestNotificationHandler_Dismiss(t *testing.T) {
	t.Run("dismisses notification by ID and returns dismissed", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+id.String()+"/dismiss", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Dismiss(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "dismissed", resp["status"])
		assert.Equal(t, id, repo.dismissedID)
	})

	t.Run("returns 400 for invalid UUID", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/not-a-uuid/dismiss", nil)
		req = withChiURLParam(req, "id", "not-a-uuid")
		w := httptest.NewRecorder()
		h.Dismiss(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 500 when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.dismissErr = fmt.Errorf("db error")
		h := adapthttp.NewNotificationHandler(repo)

		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+id.String()+"/dismiss", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Dismiss(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: WebSocket event publishing
// ---------------------------------------------------------------------------

func TestNotificationHandler_MarkRead_PublishesEvent(t *testing.T) {
	t.Run("publishes notification.read event on success", func(t *testing.T) {
		repo := newMockNotificationRepo()
		bus := &notifMockEventBus{}
		h := adapthttp.NewNotificationHandler(repo)
		h.SetEventBus(bus)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil)
		w := httptest.NewRecorder()
		h.MarkRead(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		evts := bus.published()
		require.Len(t, evts, 1)
		assert.Equal(t, "notification.read", evts[0].Type)
	})

	t.Run("does not publish event when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.markReadErr = fmt.Errorf("db error")
		bus := &notifMockEventBus{}
		h := adapthttp.NewNotificationHandler(repo)
		h.SetEventBus(bus)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil)
		w := httptest.NewRecorder()
		h.MarkRead(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Empty(t, bus.published())
	})

	t.Run("no event published when eventBus is nil", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil)
		w := httptest.NewRecorder()
		h.MarkRead(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestNotificationHandler_Dismiss_PublishesEvent(t *testing.T) {
	t.Run("publishes notification.dismissed event with correct id", func(t *testing.T) {
		repo := newMockNotificationRepo()
		bus := &notifMockEventBus{}
		h := adapthttp.NewNotificationHandler(repo)
		h.SetEventBus(bus)

		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+id.String()+"/dismiss", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Dismiss(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		evts := bus.published()
		require.Len(t, evts, 1)
		assert.Equal(t, "notification.dismissed", evts[0].Type)
		data, ok := evts[0].Data.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, id.String(), data["id"])
	})

	t.Run("does not publish event when repo fails", func(t *testing.T) {
		repo := newMockNotificationRepo()
		repo.dismissErr = fmt.Errorf("db error")
		bus := &notifMockEventBus{}
		h := adapthttp.NewNotificationHandler(repo)
		h.SetEventBus(bus)

		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+id.String()+"/dismiss", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Dismiss(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Empty(t, bus.published())
	})
}

// ---------------------------------------------------------------------------
// Isolation: owner scoping propagated to repo
// ---------------------------------------------------------------------------

// captureNotificationRepo records the OwnerID filter each method receives.
type captureNotificationRepo struct {
	capturedListOwnerID     *uuid.UUID
	capturedCountOwnerID    *uuid.UUID
	capturedMarkReadOwnerID *uuid.UUID
	capturedDismissOwnerID  *uuid.UUID
}

func (r *captureNotificationRepo) Create(_ context.Context, _ *notification.Notification) error {
	return nil
}

func (r *captureNotificationRepo) List(_ context.Context, f notification.ListFilters) ([]notification.Notification, error) {
	r.capturedListOwnerID = f.OwnerID
	return []notification.Notification{}, nil
}

func (r *captureNotificationRepo) CountUnread(_ context.Context, ownerID *uuid.UUID) (int, error) {
	r.capturedCountOwnerID = ownerID
	return 0, nil
}

func (r *captureNotificationRepo) MarkAllRead(_ context.Context, ownerID *uuid.UUID) error {
	r.capturedMarkReadOwnerID = ownerID
	return nil
}

func (r *captureNotificationRepo) Dismiss(_ context.Context, _ uuid.UUID, ownerID *uuid.UUID) error {
	r.capturedDismissOwnerID = ownerID
	return nil
}

func TestNotificationHandler_OwnerScoping(t *testing.T) {
	userID := uuid.New()
	nonAdminCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: userID,
		Role:   identity.RoleUser,
	})
	adminCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(),
		Role:   identity.RoleAdmin,
	})

	t.Run("non-admin List passes OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil).WithContext(nonAdminCtx)
		h.List(httptest.NewRecorder(), req)

		require.NotNil(t, repo.capturedListOwnerID)
		assert.Equal(t, userID, *repo.capturedListOwnerID)
	})

	t.Run("admin List passes nil OwnerID to repo (sees all)", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil).WithContext(adminCtx)
		h.List(httptest.NewRecorder(), req)

		assert.Nil(t, repo.capturedListOwnerID)
	})

	t.Run("no identity in context yields nil OwnerID (local mode)", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
		h.List(httptest.NewRecorder(), req)

		assert.Nil(t, repo.capturedListOwnerID)
	})

	t.Run("non-admin UnreadCount passes OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil).WithContext(nonAdminCtx)
		h.UnreadCount(httptest.NewRecorder(), req)

		require.NotNil(t, repo.capturedCountOwnerID)
		assert.Equal(t, userID, *repo.capturedCountOwnerID)
	})

	t.Run("admin UnreadCount passes nil OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil).WithContext(adminCtx)
		h.UnreadCount(httptest.NewRecorder(), req)

		assert.Nil(t, repo.capturedCountOwnerID)
	})

	t.Run("non-admin MarkRead passes OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil).WithContext(nonAdminCtx)
		h.MarkRead(httptest.NewRecorder(), req)

		require.NotNil(t, repo.capturedMarkReadOwnerID)
		assert.Equal(t, userID, *repo.capturedMarkReadOwnerID)
	})

	t.Run("admin MarkRead passes nil OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/mark-read", nil).WithContext(adminCtx)
		h.MarkRead(httptest.NewRecorder(), req)

		assert.Nil(t, repo.capturedMarkReadOwnerID)
	})

	t.Run("non-admin Dismiss passes OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		notifID := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+notifID.String()+"/dismiss", nil).WithContext(nonAdminCtx)
		req = withChiURLParam(req, "id", notifID.String())
		h.Dismiss(httptest.NewRecorder(), req)

		require.NotNil(t, repo.capturedDismissOwnerID)
		assert.Equal(t, userID, *repo.capturedDismissOwnerID)
	})

	t.Run("admin Dismiss passes nil OwnerID to repo", func(t *testing.T) {
		repo := &captureNotificationRepo{}
		h := adapthttp.NewNotificationHandler(repo)

		notifID := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/"+notifID.String()+"/dismiss", nil).WithContext(adminCtx)
		req = withChiURLParam(req, "id", notifID.String())
		h.Dismiss(httptest.NewRecorder(), req)

		assert.Nil(t, repo.capturedDismissOwnerID)
	})
}
