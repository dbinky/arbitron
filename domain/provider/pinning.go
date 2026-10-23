package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Pin-record discriminators. They mirror instance.ModelPinsVersionLegacy and
// instance.ModelPinsVersionSnapshot; the provider package does not import
// instance.
const (
	PinVersionLegacy   = 0
	PinVersionSnapshot = 1
)

// ErrPinUnavailable marks a post-migration run whose pin record cannot supply a
// model for the requested alias (missing, empty, or an unsupported version). It
// never falls back to live resolution: that would silently run the live model
// on a run promised its trigger-time one.
var ErrPinUnavailable = errors.New("provider: run model pin unavailable")

// PinSet is a run's persisted pin record.
type PinSet struct {
	Version int
	Models  map[string]string
	// OpaqueRoutes mirrors the persisted native D8 exceptions. An absent
	// exception retains strict exact-model enforcement.
	OpaqueRoutes map[string]string
	// OpaqueConnections holds the trigger-time connection_ref per OpaqueRoutes
	// alias. A native exception without one is refused, never resolved live.
	OpaqueConnections map[string]string
}

// PinLookup reads a run's pins. Release drops any cached entry once the run is
// terminal; a released run reloads from the repository if read again.
type PinLookup interface {
	Lookup(ctx context.Context, instanceID uuid.UUID) (PinSet, error)
	Release(instanceID uuid.UUID)
}

// InstanceIDFunc extracts the running instance from a context
// (engine.InstanceIDFromContext).
type InstanceIDFunc func(context.Context) (uuid.UUID, bool)

// PinningResolver decorates the live ProfileResolver: inside a post-migration
// run it replaces only binding.Model with the model pinned at trigger.
// Credentials, connection, backend, axes and tools still resolve live, except
// that a D8 native pin refuses a profile now naming another connection: the
// connection locates the configured deployment the run was pinned to.
// Outside a run, and for a legacy (version 0) run, it resolves live.
type PinningResolver struct {
	live       ProfileResolver
	lookup     PinLookup
	instanceID InstanceIDFunc
	requireRun bool
}

// NewPinningResolver builds the decorator. All three collaborators are required.
func NewPinningResolver(live ProfileResolver, lookup PinLookup, instanceID InstanceIDFunc) *PinningResolver {
	if live == nil || lookup == nil || instanceID == nil {
		panic("provider: NewPinningResolver requires live resolver, pin lookup and instance id func")
	}
	return &PinningResolver{live: live, lookup: lookup, instanceID: instanceID}
}

// ForRun returns an execution-only view that refuses a missing or zero run ID.
// It shares the live resolver and pin lookup without changing this resolver's
// out-of-run behaviour. Legacy runs with a valid ID still resolve live.
func (r *PinningResolver) ForRun() *PinningResolver {
	view := *r
	view.requireRun = true
	return &view
}

// Resolve implements ProfileResolver.
func (r *PinningResolver) Resolve(ctx context.Context, alias string) (ResolvedBinding, error) {
	id, inRun := r.instanceID(ctx)
	if r.requireRun && (!inRun || id == uuid.Nil) {
		return ResolvedBinding{}, fmt.Errorf("%w: instance ID required for run profile resolution", ErrPinUnavailable)
	}
	if !inRun {
		return r.live.Resolve(ctx, alias)
	}
	pins, err := r.lookup.Lookup(ctx, id)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("provider: load model pins for run %s: %w", id, err)
	}
	for pinAlias, route := range pins.OpaqueRoutes {
		if pins.Version != PinVersionSnapshot || pinAlias == "" || pins.Models[pinAlias] == "" ||
			(route != CanonicalAzure && route != CanonicalBedrock) {
			return ResolvedBinding{}, fmt.Errorf("%w: run %s has invalid native route exception", ErrPinUnavailable, id)
		}
	}
	for pinAlias, conn := range pins.OpaqueConnections {
		if conn == "" || pins.OpaqueRoutes[pinAlias] == "" {
			return ResolvedBinding{}, fmt.Errorf("%w: run %s has invalid native route exception", ErrPinUnavailable, id)
		}
	}
	switch pins.Version {
	case PinVersionLegacy:
		if len(pins.Models) != 0 {
			return ResolvedBinding{}, fmt.Errorf("%w: run %s has pins but no pin version", ErrPinUnavailable, id)
		}
		return r.live.Resolve(ctx, alias)
	case PinVersionSnapshot:
	default:
		return ResolvedBinding{}, fmt.Errorf("%w: run %s has unsupported pin version %d", ErrPinUnavailable, id, pins.Version)
	}
	model := pins.Models[alias]
	if model == "" {
		return ResolvedBinding{}, fmt.Errorf("%w: run %s has no pinned model for profile %q", ErrPinUnavailable, id, alias)
	}
	binding, err := r.live.Resolve(ctx, alias)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if pins.OpaqueRoutes[alias] != "" {
		want := pins.OpaqueConnections[alias]
		if want == "" {
			return ResolvedBinding{}, fmt.Errorf("%w: run %s native route exception for profile %q has no trigger-time connection", ErrPinUnavailable, id, alias)
		}
		if binding.ConnectionRef != want {
			return ResolvedBinding{}, fmt.Errorf("%w: pinned native route exception is incompatible with live route: profile %q connection changed since trigger", ErrPinUnavailable, alias)
		}
	}
	binding.Model = model
	binding.Pinned = true
	binding.OpaquePinRoute = pins.OpaqueRoutes[alias]
	return binding, nil
}
