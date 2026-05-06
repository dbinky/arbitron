package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
)

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

func (r *mockNotificationRepo) CountUnread(_ context.Context) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.unreadCount, nil
}

func (r *mockNotificationRepo) MarkAllRead(_ context.Context) error {
	return r.markReadErr
}

func (r *mockNotificationRepo) Dismiss(_ context.Context, id uuid.UUID) error {
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

// ---------------------------------------------------------------------------
// Tests: List
// ---------------------------------------------------------------------------

func TestNotificationHandler_List(t *testing.T) {
	t.Run("returns empty array when no notifications", func(t *testing.T) {
		repo := newMockNotificationRepo()
		h := adapthttp.NewNotificationHandler(repo)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp)
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
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp, 1)
		assert.Equal(t, n.ID.String(), resp[0]["id"])
		assert.Equal(t, n.InstanceID.String(), resp[0]["instanceId"])
		assert.Equal(t, "my-workflow", resp[0]["workflowName"])
		assert.Equal(t, "completed", resp[0]["type"])
		assert.Equal(t, false, resp[0]["read"])
		assert.Equal(t, false, resp[0]["dismissed"])
		assert.NotEmpty(t, resp[0]["createdAt"])
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
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp, 1)
		assert.Equal(t, dismissed.ID.String(), resp[0]["id"])
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
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Len(t, resp, 2)
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
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.LessOrEqual(t, len(resp), 100)
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
