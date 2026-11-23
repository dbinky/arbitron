package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Dispatcher is the StepDispatcher slice the confirmer needs; *runner.StepDispatcher satisfies it.
type Dispatcher interface {
	Dispatch(ctx context.Context, req runner.ExecRequirements, job runner.StepJob) (runner.StepOutput, error)
}

// ProfileLister lists configured profiles; *sqlite.ProfileRepo satisfies it.
type ProfileLister interface {
	List(ctx context.Context) ([]provider.Profile, error)
}

var (
	ErrNoConfirmationModel  = errors.New("probe: no subscription profile to confirm with")
	ErrConfirmerNotAttached = errors.New("probe: confirmer has no dispatcher")
)

// confirmNamespace derives the synthetic per-source run id for probe jobs.
var confirmNamespace = uuid.MustParse("3b2f7d64-5f0e-4c3a-9f61-7b1f0c2d8e4a")

// DispatchConfirmer sends a 1-token exact-model chat THROUGH the dispatcher,
// pinned to the source's runner, so it reserves a slot like any step and can
// never oversubscribe. Phase 1: Anthropic subscriptions only (OpenAI ones are
// confirmed read-only via ordinaryUsageAllowed).
type DispatchConfirmer struct {
	mu       sync.RWMutex
	d        Dispatcher
	profiles ProfileLister
	workDir  string
	timeout  time.Duration
}

func NewDispatchConfirmer(profiles ProfileLister, workDir string, timeout time.Duration) *DispatchConfirmer {
	if profiles == nil || workDir == "" || timeout <= 0 {
		panic("probe: NewDispatchConfirmer requires profiles, a work dir and a positive timeout")
	}
	return &DispatchConfirmer{profiles: profiles, workDir: workDir, timeout: timeout}
}

// Attach wires the dispatcher after it is built (the dispatcher depends on the
// monitor, which depends on this confirmer).
func (c *DispatchConfirmer) Attach(d Dispatcher) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d = d
}

func (c *DispatchConfirmer) Confirm(ctx context.Context, s runner.Source) error {
	if s.Kind != runner.CredAnthropicSubscription {
		return runner.ErrConfirmationUnsupported
	}
	c.mu.RLock()
	d := c.d
	c.mu.RUnlock()
	if d == nil {
		return ErrConfirmerNotAttached
	}
	b, group, err := c.pick(ctx, s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.workDir, 0o700); err != nil {
		return fmt.Errorf("probe: confirmation work dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	id := engine.StepIdentity{
		RunID: uuid.NewSHA1(confirmNamespace, []byte(s.ID)), NodeID: "probe:" + string(s.ID),
		InvocationID: uuid.New(), OperationID: "probe/confirm", Kind: engine.OperationProbe, Attempt: 1,
	}
	ctx = engine.ContextWithStepIdentity(ctx, id)
	req := runner.ExecRequirements{
		Group: group, Model: b.Model, Op: runner.OpChat, Affinity: runner.AffinityBuiltIn,
		RunID: id.RunID, PinnedRunner: s.RunnerID, RequiredSource: s.ID,
	}
	chat := engine.ChatRequest{Model: b.Model, Prompt: "Reply with OK.", MaxTokens: 1, WorkDir: c.workDir, SuppressTools: true}
	_, err = d.Dispatch(ctx, req, runner.StepJob{Op: runner.OpChat, Binding: b, Chat: &chat})
	return err
}

// pick chooses the first (alias-sorted) no-connection profile whose exact model
// is the source's persisted ConfirmationModel and whose group matches the source.
func (c *DispatchConfirmer) pick(ctx context.Context, s runner.Source) (provider.ResolvedBinding, runner.ModelGroup, error) {
	ps, err := c.profiles.List(ctx)
	if err != nil {
		return provider.ResolvedBinding{}, runner.ModelGroup{}, fmt.Errorf("probe: list profiles: %w", err)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Alias < ps[j].Alias })
	for _, p := range ps {
		if p.ConnectionRef != "" || p.Model == "" || s.Health.ConfirmationModel == "" || p.Model != s.Health.ConfirmationModel {
			continue
		}
		b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: p.Model, AllowedTools: append([]string(nil), p.AllowedTools...), ContextWindow: p.ContextWindow, Effort: p.Effort}
		g, err := runner.GroupFor(b, runner.OpChat)
		if err != nil || g.Kind != s.Kind {
			continue
		}
		for _, f := range s.Families {
			if f == g.Family {
				return b, g, nil
			}
		}
	}
	return provider.ResolvedBinding{}, runner.ModelGroup{}, ErrNoConfirmationModel
}
