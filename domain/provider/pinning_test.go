package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/provider"
)

type stubLive struct {
	model string
	err   error
	calls int
}

func (s *stubLive) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	s.calls++
	if s.err != nil {
		return provider.ResolvedBinding{}, s.err
	}
	return provider.ResolvedBinding{Provider: "anthropic", BaseURL: "https://live", Backend: "claude-code", Model: s.model, AllowedTools: []string{"Read"}}, nil
}

type stubLookup struct {
	set   provider.PinSet
	err   error
	calls int
}

func (s *stubLookup) Lookup(context.Context, uuid.UUID) (provider.PinSet, error) {
	s.calls++
	return s.set, s.err
}
func (s *stubLookup) Release(uuid.UUID) {}

func inRun(id uuid.UUID) provider.InstanceIDFunc {
	return func(context.Context) (uuid.UUID, bool) { return id, true }
}

func outOfRun(context.Context) (uuid.UUID, bool) { return uuid.Nil, false }

func TestPinningResolverReplacesOnlyModel(t *testing.T) {
	live := &stubLive{model: "version-Y"}
	lk := &stubLookup{set: provider.PinSet{Version: provider.PinVersionSnapshot, Models: map[string]string{"a": "version-X"}}}
	r := provider.NewPinningResolver(live, lk, inRun(uuid.New()))
	b, err := r.Resolve(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if b.Model != "version-X" {
		t.Fatalf("model = %q, want version-X", b.Model)
	}
	if b.BaseURL != "https://live" || b.Backend != "claude-code" || len(b.AllowedTools) != 1 {
		t.Fatalf("live fields not preserved: %+v", b)
	}
}

func TestPinningResolverLegacyAndOutOfRunResolveLive(t *testing.T) {
	live := &stubLive{model: "version-Y"}
	lk := &stubLookup{set: provider.PinSet{Version: provider.PinVersionLegacy}}
	b, err := provider.NewPinningResolver(live, lk, inRun(uuid.New())).Resolve(context.Background(), "a")
	if err != nil || b.Model != "version-Y" {
		t.Fatalf("legacy: %+v %v", b, err)
	}
	lk2 := &stubLookup{}
	b, err = provider.NewPinningResolver(live, lk2, outOfRun).Resolve(context.Background(), "a")
	if err != nil || b.Model != "version-Y" || lk2.calls != 0 {
		t.Fatalf("out of run: %+v %v lookups=%d", b, err, lk2.calls)
	}
}

func TestPinningResolverFailsClosed(t *testing.T) {
	cases := map[string]struct {
		set provider.PinSet
		err error
	}{
		"missing alias":         {set: provider.PinSet{Version: 1, Models: map[string]string{"other": "x"}}},
		"empty model":           {set: provider.PinSet{Version: 1, Models: map[string]string{"a": ""}}},
		"nil pins version 1":    {set: provider.PinSet{Version: 1}},
		"unsupported version":   {set: provider.PinSet{Version: 7, Models: map[string]string{"a": "x"}}},
		"pins on legacy":        {set: provider.PinSet{Version: 0, Models: map[string]string{"a": "x"}}},
		"legacy exception":      {set: provider.PinSet{Version: 0, OpaqueRoutes: map[string]string{"a": "azure"}}},
		"unknown exception":     {set: provider.PinSet{Version: 1, Models: map[string]string{"a": "x"}, OpaqueRoutes: map[string]string{"a": "openai"}}},
		"exception without pin": {set: provider.PinSet{Version: 1, Models: map[string]string{"a": "x"}, OpaqueRoutes: map[string]string{"other": "azure"}}},
		"empty exception alias": {set: provider.PinSet{Version: 1, Models: map[string]string{"a": "x", "": "x"}, OpaqueRoutes: map[string]string{"": "azure"}}},
		"lookup fault":          {err: errors.New("db down")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			live := &stubLive{model: "version-Y"}
			r := provider.NewPinningResolver(live, &stubLookup{set: tc.set, err: tc.err}, inRun(uuid.New()))
			b, err := r.Resolve(context.Background(), "a")
			if err == nil {
				t.Fatalf("expected error, got binding %+v", b)
			}
			if b.Model != "" {
				t.Fatalf("binding leaked on error: %+v", b)
			}
		})
	}
}

func TestPinningResolverPropagatesLiveError(t *testing.T) {
	live := &stubLive{err: provider.ErrUnknownProfile}
	lk := &stubLookup{set: provider.PinSet{Version: 1, Models: map[string]string{"a": "x"}}}
	_, err := provider.NewPinningResolver(live, lk, inRun(uuid.New())).Resolve(context.Background(), "a")
	if !errors.Is(err, provider.ErrUnknownProfile) {
		t.Fatalf("err = %v", err)
	}
}

func TestPinningResolverMarksPinnedOnlyWhenPinApplied(t *testing.T) {
	live := &stubLive{model: "version-Y"}
	lk := &stubLookup{set: provider.PinSet{Version: provider.PinVersionSnapshot, Models: map[string]string{"a": "version-X"}}}
	b, err := provider.NewPinningResolver(live, lk, inRun(uuid.New())).Resolve(context.Background(), "a")
	if err != nil || !b.Pinned {
		t.Fatalf("pinned run: %+v %v", b, err)
	}
	b, err = provider.NewPinningResolver(live, &stubLookup{set: provider.PinSet{Version: provider.PinVersionLegacy}}, inRun(uuid.New())).Resolve(context.Background(), "a")
	if err != nil || b.Pinned {
		t.Fatalf("legacy run must not be marked pinned: %+v %v", b, err)
	}
}

func TestPinningResolverForRunRequiresInstanceContext(t *testing.T) {
	for name, extract := range map[string]provider.InstanceIDFunc{
		"missing": outOfRun,
		"zero":    inRun(uuid.Nil),
	} {
		t.Run(name, func(t *testing.T) {
			live := &stubLive{model: "version-Y"}
			lookup := &stubLookup{}
			r := provider.NewPinningResolver(live, lookup, extract)
			binding, err := r.ForRun().Resolve(context.Background(), "a")
			if !errors.Is(err, provider.ErrPinUnavailable) || binding.Model != "" {
				t.Fatalf("missing run context must refuse resolution: binding=%+v err=%v", binding, err)
			}
			if live.calls != 0 || lookup.calls != 0 {
				t.Fatalf("missing run context reached collaborators: live=%d lookup=%d", live.calls, lookup.calls)
			}
			// Creating a run-only view must not change the original resolver's
			// intentional out-of-run behaviour.
			if name == "missing" {
				binding, err = r.Resolve(context.Background(), "a")
				if err != nil || binding.Model != "version-Y" {
					t.Fatalf("out-of-run resolution changed: %+v %v", binding, err)
				}
			}
		})
	}
}

func TestPinningResolverForRunPreservesPinAndLegacySemantics(t *testing.T) {
	for name, tc := range map[string]struct {
		set    provider.PinSet
		model  string
		pinned bool
		err    error
	}{
		"snapshot": {set: provider.PinSet{Version: 1, Models: map[string]string{"a": "version-X"}}, model: "version-X", pinned: true},
		"legacy":   {set: provider.PinSet{Version: 0}, model: "version-Y"},
		"empty":    {set: provider.PinSet{Version: 1}},
		"corrupt":  {set: provider.PinSet{Version: 9}},
		"db fault": {err: errors.New("db down")},
	} {
		t.Run(name, func(t *testing.T) {
			live := &stubLive{model: "version-Y"}
			lookup := &stubLookup{set: tc.set, err: tc.err}
			r := provider.NewPinningResolver(live, lookup, inRun(uuid.New())).ForRun()
			binding, err := r.Resolve(context.Background(), "a")
			if tc.model == "" {
				if err == nil || binding.Model != "" || live.calls != 0 {
					t.Fatalf("invalid pin must refuse live resolution: %+v %v live=%d", binding, err, live.calls)
				}
				if tc.err != nil && !errors.Is(err, tc.err) {
					t.Fatalf("lookup error not preserved: %v", err)
				}
				return
			}
			if err != nil || binding.Model != tc.model || binding.Pinned != tc.pinned || lookup.calls != 1 {
				t.Fatalf("run resolution: %+v %v lookups=%d", binding, err, lookup.calls)
			}
		})
	}
}

type stubOpaqueLive struct{ conn string }

func (s stubOpaqueLive) Resolve(context.Context, string) (provider.ResolvedBinding, error) {
	return provider.ResolvedBinding{Provider: "azure", BaseURL: "https://" + s.conn, ConnectionRef: s.conn, Model: "live-deployment"}, nil
}

// Paired row 4: a D8 pin executes only on the connection captured at trigger.
// A profile edit to another connection, even of the same family, is refused,
// and a family-only record (no captured connection) never resolves live.
func TestPinningResolverOpaquePinBindsTriggerConnection(t *testing.T) {
	pinned := func(conns map[string]string) provider.PinSet {
		return provider.PinSet{Version: 1, Models: map[string]string{"a": "configured-X"},
			OpaqueRoutes: map[string]string{"a": "azure"}, OpaqueConnections: conns}
	}
	cases := map[string]struct {
		set    provider.PinSet
		live   string
		errMsg string
	}{
		"same connection":            {set: pinned(map[string]string{"a": "conn-a"}), live: "conn-a"},
		"profile moved connection":   {set: pinned(map[string]string{"a": "conn-a"}), live: "conn-b", errMsg: "connection changed since trigger"},
		"profile cleared connection": {set: pinned(map[string]string{"a": "conn-a"}), live: "", errMsg: "connection changed since trigger"},
		"family-only record":         {set: pinned(nil), live: "conn-a", errMsg: "no trigger-time connection"},
		"empty captured connection":  {set: pinned(map[string]string{"a": ""}), live: "", errMsg: "invalid native route exception"},
		"connection without route":   {set: provider.PinSet{Version: 1, Models: map[string]string{"a": "x"}, OpaqueConnections: map[string]string{"a": "conn-a"}}, live: "conn-a", errMsg: "invalid native route exception"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := provider.NewPinningResolver(stubOpaqueLive{conn: tc.live}, &stubLookup{set: tc.set}, inRun(uuid.New()))
			b, err := r.Resolve(context.Background(), "a")
			if tc.errMsg == "" {
				if err != nil || b.Model != "configured-X" || b.OpaquePinRoute != "azure" || b.ConnectionRef != tc.live {
					t.Fatalf("binding %+v err %v", b, err)
				}
				return
			}
			if err == nil || !errors.Is(err, provider.ErrPinUnavailable) || !strings.Contains(err.Error(), tc.errMsg) || b.Model != "" {
				t.Fatalf("want ErrPinUnavailable containing %q, got binding %+v err %v", tc.errMsg, b, err)
			}
		})
	}
}
