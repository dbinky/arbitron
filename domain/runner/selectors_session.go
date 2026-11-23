package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// streamSelectorFor returns the session-aware routed stream selector when a
// pin lookup is wired, else the Phase 1 selector (AffinityBuiltIn).
func streamSelectorFor(inner engine.InteractiveStreamSelector, d dispatcher, pins SessionPins) engine.InteractiveStreamSelector {
	if pins == nil {
		return newRoutingStreamSelector(inner, d)
	}
	if inner == nil || d == nil {
		panic("runner: session routing stream selector requires inner selector and dispatcher")
	}
	return &sessionRoutingStreamSelector{inner: inner, d: d, pins: pins}
}

type sessionRoutingStreamSelector struct {
	inner engine.InteractiveStreamSelector
	d     dispatcher
	pins  SessionPins
}

func (s *sessionRoutingStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	if _, err := s.inner.StreamProviderFor(b); err != nil {
		return nil, err
	}
	return &sessionRoutedStream{routedChat: routedChat{d: s.d, b: b, affinity: AffinitySession}, pins: s.pins}, nil
}

// sessionRoutedStream is routedChat whose StreamChat places on the session pin.
type sessionRoutedStream struct {
	routedChat
	pins SessionPins
}

func (p *sessionRoutedStream) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	er, err := requirementsFor(ctx, p.b, OpType("stream"), AffinitySession, req.RequireExecutedModel, req.ToolScope)
	if err != nil {
		return nil, err
	}
	if er, err = sessionStreamRequirements(ctx, p.pins, p.b, er, req); err != nil {
		return nil, err
	}
	r := req
	out, err := p.d.Dispatch(ctx, er, StepJob{Op: OpType("stream"), Binding: p.b, Chat: &r})
	if err != nil {
		return nil, err
	}
	if out.Stream == nil {
		return nil, errMissingStepOutput
	}
	return out.Stream, nil
}

// sessionStreamRequirements sets the session affinity and pin of one turn.
func sessionStreamRequirements(ctx context.Context, pins SessionPins, b provider.ResolvedBinding, er ExecRequirements, req engine.ChatRequest) (ExecRequirements, error) {
	if b.Kind == provider.ProfileKindChat && b.ConnectionRef != "" {
		er.Affinity = AffinityNone // API / LunaRoute chat: no harness session file
		return er, nil
	}
	si, ok := engine.StepIdentityFromContext(ctx)
	if !ok {
		return er, engine.ErrMissingStepIdentity
	}
	if strings.HasPrefix(si.OperationID, "resolution/") {
		er.Affinity = AffinityBuiltIn // staging-resource pinning: P4 T11
		return er, nil
	}
	pin, found, err := pins.PinFor(ctx, si.RunID, si.NodeID)
	if err != nil {
		return er, fmt.Errorf("route interactive turn: %w", err)
	}
	er.Affinity = AffinitySession
	switch {
	case found:
		er.PinnedRunner = pin
	case req.SessionID != "":
		er.PinnedRunner = BuiltInRunnerID // pre-Phase-4 session: its file is on the server
	}
	return er, nil
}
