package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ---------------------------------------------------------------------------
// End-to-end engine -> Teams seam (GAP-003 / focus area 3)
//
// These tests exercise the real seam the spec's headline outcome depends on:
//   engine.emitNotification -> NewDefaultExternalNotifier (MultiNotifier ->
//   TeamsNotifier) -> webhook HTTP POST.
// The other engine external-notifier tests stop at a mock ExternalNotifier and
// default_notifier_test.go starts at the factory; neither proves that driving a
// real Engine produces a Teams card for each notification-raising event.
// ---------------------------------------------------------------------------

// teamsStub is an httptest webhook endpoint that records every Adaptive Card it
// receives. It returns a configurable status so a non-2xx (best-effort failure)
// path can be exercised too.
type teamsStub struct {
	mu     sync.Mutex
	status int
	cards  []stubCard
}

// stubCard captures only the parts of the Teams Workflows envelope the tests
// assert on: the FactSet values and the deep-link action URL.
type stubCard struct {
	Attachments []struct {
		Content struct {
			Body []struct {
				Facts []struct {
					Title string `json:"title"`
					Value string `json:"value"`
				} `json:"facts"`
			} `json:"body"`
			Actions []struct {
				URL string `json:"url"`
			} `json:"actions"`
		} `json:"content"`
	} `json:"attachments"`
}

func newTeamsStub() (*teamsStub, *httptest.Server) {
	s := &teamsStub{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var card stubCard
		_ = json.Unmarshal(body, &card)
		s.mu.Lock()
		s.cards = append(s.cards, card)
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	return s, srv
}

func (s *teamsStub) setStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

// fact returns the value of the named FactSet entry of the most recent card, or
// "" if no card (or no such fact) has been received.
func (s *teamsStub) lastFact(title string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cards) == 0 {
		return "", false
	}
	last := s.cards[len(s.cards)-1]
	for _, att := range last.Attachments {
		for _, el := range att.Content.Body {
			for _, f := range el.Facts {
				if f.Title == title {
					return f.Value, true
				}
			}
		}
	}
	return "", false
}

func (s *teamsStub) lastActionURL() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cards) == 0 {
		return "", false
	}
	last := s.cards[len(s.cards)-1]
	for _, att := range last.Attachments {
		for _, a := range att.Content.Actions {
			return a.URL, true
		}
	}
	return "", false
}

func (s *teamsStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cards)
}

// waitForTeamsCard polls until the stub has received a card whose Event fact
// matches notifType, or the timeout expires.
func waitForTeamsCard(t *testing.T, stub *teamsStub, notifType notification.Type, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for Teams card of type %q (got %d cards)", notifType, stub.count())
		default:
			if v, ok := stub.lastFact("Event"); ok && v == string(notifType) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// teamsCfg builds a Config with Teams forwarding enabled, pointing at the stub.
func teamsCfg(webhookURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{PublicBaseURL: "http://dash.example"},
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: webhookURL, Enabled: true},
		},
	}
}

// interactiveWorkflow is a single interactive node that waits for input, which
// drives emitNotification(TypeNeedsInteraction).
func interactiveWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "interactive-wf",
		Description: "A workflow that pauses for interaction",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeInteractive, Entrypoint: true},
		},
	}
}

// Happy / success: a completed run forwards a Teams card carrying the event,
// workflow, instance and a working deep link.
func TestEngine_TeamsSeam_CompletedForwardsCard(t *testing.T) {
	stub, srv := newTeamsStub()
	defer srv.Close()

	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.engine.SetExternalNotifier(adaptnotif.NewDefaultExternalNotifier(teamsCfg(srv.URL), logger))

	id, _, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	waitForTeamsCard(t, stub, notification.TypeCompleted, 2*time.Second)

	v, _ := stub.lastFact("Event")
	assert.Equal(t, string(notification.TypeCompleted), v)
	wf, _ := stub.lastFact("Workflow")
	assert.Equal(t, "linear-wf", wf)
	inst, _ := stub.lastFact("Instance")
	assert.Equal(t, id.String(), inst)
	url, ok := stub.lastActionURL()
	require.True(t, ok, "card must carry a deep-link action when a public base URL is set")
	assert.Equal(t, "http://dash.example/instances/"+id.String(), url)
}

// Failure: a failed run forwards a card with the failed event type.
func TestEngine_TeamsSeam_FailedForwardsCard(t *testing.T) {
	stub, srv := newTeamsStub()
	defer srv.Close()

	f := newTestFixtures()
	f.workflowRepo.add(errorWorkflow(workflow.ErrorBehavior{Type: "halt"}))
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.engine.SetExternalNotifier(adaptnotif.NewDefaultExternalNotifier(teamsCfg(srv.URL), logger))

	id, _, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	waitForTeamsCard(t, stub, notification.TypeFailed, 2*time.Second)

	v, _ := stub.lastFact("Event")
	assert.Equal(t, string(notification.TypeFailed), v)
	inst, _ := stub.lastFact("Instance")
	assert.Equal(t, id.String(), inst)
}

// Edge: an interactive node that waits for input forwards a needs_interaction
// card — the third notification-raising event type.
func TestEngine_TeamsSeam_NeedsInteractionForwardsCard(t *testing.T) {
	stub, srv := newTeamsStub()
	defer srv.Close()

	f := newTestFixtures()
	f.workflowRepo.add(interactiveWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		id, _ := engine.InstanceIDFromContext(ctx)
		_ = f.instanceRepo.UpdateState(ctx, id, instance.StateWaiting)
		return &engine.NodeResult{IsWait: true}, nil
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.engine.SetExternalNotifier(adaptnotif.NewDefaultExternalNotifier(teamsCfg(srv.URL), logger))

	id, _, err := f.engine.Trigger("interactive-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)
	waitForTeamsCard(t, stub, notification.TypeNeedsInteraction, 2*time.Second)

	v, _ := stub.lastFact("Event")
	assert.Equal(t, string(notification.TypeNeedsInteraction), v)
	inst, _ := stub.lastFact("Instance")
	assert.Equal(t, id.String(), inst)
}

// Error/best-effort: the in-app notification path (repo Create + the
// notification.created event) fires regardless of the Teams delivery outcome.
// Teams is *in addition to*, never a replacement for, the in-app bell.
func TestEngine_TeamsSeam_InAppFiresWhenTeamsFails(t *testing.T) {
	stub, srv := newTeamsStub()
	defer srv.Close()
	stub.setStatus(http.StatusInternalServerError) // webhook rejects every delivery

	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.engine.SetExternalNotifier(adaptnotif.NewDefaultExternalNotifier(teamsCfg(srv.URL), logger))

	id, _, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State, "instance must still complete when Teams delivery fails")

	// The in-app notification was persisted...
	var found bool
	for _, n := range notifRepo.getCreated() {
		if n.InstanceID == id && n.Type == notification.TypeCompleted {
			found = true
		}
	}
	assert.True(t, found, "in-app notification must be created even when Teams forwarding fails")

	// ...and the in-app bell event was published.
	created := f.eventBus.eventsOfType(id, "notification.created")
	assert.NotEmpty(t, created, "notification.created event must fire even when Teams forwarding fails")

	// Teams was attempted (best-effort), then swallowed without disrupting the run.
	assert.GreaterOrEqual(t, stub.count(), 1, "Teams delivery should have been attempted")
}
