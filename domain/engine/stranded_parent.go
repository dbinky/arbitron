package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// enterRunning writes id `running` and registers its walker context as ONE step
// under e.walkersMu, so no reader holding walkersMu (AdoptStrandedParent) can
// observe a running run without its walker. On a write error nothing is
// registered.
func (e *Engine) enterRunning(ctx context.Context, id uuid.UUID) (context.Context, error) {
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	if err := e.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
		return nil, err
	}
	return e.registerWalkerLocked(id), nil
}

// walkerReg is one walker's registration in e.contexts. Its pointer identity
// is what lets a walker remove only its OWN entry: a successor registered under
// the same instance id (Resume, Retry) is a different *walkerReg.
//
// e.contexts and every state write that must stay atomic with it are guarded by
// Engine.walkersMu (see its doc); executor and group configuration stays on
// Engine.mu, which no walkersMu holder may take.
type walkerReg struct {
	cancel context.CancelFunc
}

type walkerRegKey struct{}

// registerWalkerLocked registers a fresh walker for id and returns its run
// context, which carries the registration. Callers hold e.walkersMu.
func (e *Engine) registerWalkerLocked(id uuid.UUID) context.Context {
	runCtx, cancel := context.WithCancel(context.Background())
	reg := &walkerReg{cancel: cancel}
	e.contexts[id] = reg
	return context.WithValue(runCtx, walkerRegKey{}, reg)
}

// deregisterWalkerLocked removes id's registration only if it is the one
// carried by runCtx (the calling walker's own). Callers hold e.walkersMu.
func (e *Engine) deregisterWalkerLocked(runCtx context.Context, id uuid.UUID) {
	mine, _ := runCtx.Value(walkerRegKey{}).(*walkerReg)
	if mine != nil && e.contexts[id] == mine {
		delete(e.contexts, id)
	}
}

// AdoptStrandedParent rescues a parent left in `running` at the awaited spawn
// node nodeID with no walker driving it: the state a pre-fix resume left the
// 2026-11-19 feature-pipeline run in, where the child's terminal wake skipped a
// non-waiting parent and nothing else would ever move it. Persisted `running`
// rows from a crash never reach this path: RecoverCrashedInstances fails them
// before startup reconciliation. It rescues parents stranded while serving, and
// is driven by the reconciler's periodic sweep.
//
// It moves the parent `running -> waiting` (conditional on both state and
// node, SpawnWaitRepository.SuspendStrandedAt) so the ordinary spawn wake
// machinery can re-queue it; on re-entry the spawn node short-circuits on the
// terminal link and routes success or failure. It reports whether it moved the
// parent.
//
// The live-walker check and the write happen under e.walkersMu. Every path that
// puts a run into `running` and registers its walker does so under the same
// lock (StartInstance, Retry, resumeFromRunning), so a run that is merely
// starting can never be mistaken for a stranded one. A parent with a live
// walker is never touched: adopting it would hand one run to two walkers.
//
// SINGLE SERVING RUNTIME PER DATABASE. "Live walker" means an entry in THIS
// process's registry. Walker ownership is process-local, so this is only sound
// while exactly one serving runtime (HTTP `serve` or standalone MCP) owns the
// database. A second runtime on the same database would see the first's live
// parents as walkerless and adopt them, running one run under two walkers.
// Nothing enforces that yet; the deployment must.
func (e *Engine) AdoptStrandedParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error) {
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	if e.spawnWaits == nil {
		return false, fmt.Errorf("adopting stranded parent %s: engine has no SpawnWaitRepository (SetSpawnWaits)", parentID)
	}
	if _, live := e.contexts[parentID]; live {
		return false, nil
	}
	ok, err := e.spawnWaits.SuspendStrandedAt(ctx, parentID, nodeID)
	if err != nil {
		return false, fmt.Errorf("adopting stranded parent %s: %w", parentID, err)
	}
	return ok, nil
}

// SetSpawnWaits wires the repository AdoptStrandedParent suspends through. It
// is required for adoption; a nil repository is rejected so a missed wiring
// step cannot silently disable the stranded-parent safety net. Configure before
// serving.
func (e *Engine) SetSpawnWaits(repo instance.SpawnWaitRepository) {
	if repo == nil {
		panic("engine: SetSpawnWaits requires a non-nil SpawnWaitRepository")
	}
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	e.spawnWaits = repo
}
