package provider

import "testing"

// EnableLunaRouteBackendsForTest temporarily enables backends in this package.
// Tests mutating the phase gate must not run in parallel.
func EnableLunaRouteBackendsForTest(t *testing.T, backends ...string) {
	t.Helper()
	saved := lunaRouteEnabledBackends
	t.Cleanup(func() { lunaRouteEnabledBackends = saved })
	next := map[string]bool{}
	for k, v := range saved {
		next[k] = v
	}
	for _, b := range backends {
		next[b] = true
	}
	lunaRouteEnabledBackends = next
}

func DisableLunaRouteBackendsForTest(t *testing.T, backends ...string) {
	t.Helper()
	saved := lunaRouteEnabledBackends
	t.Cleanup(func() { lunaRouteEnabledBackends = saved })
	next := map[string]bool{}
	for k, v := range saved {
		next[k] = v
	}
	for _, b := range backends {
		delete(next, b)
	}
	lunaRouteEnabledBackends = next
}

func TestLunaRouteEnabledBackends_ShippedSet(t *testing.T) {
	want := map[string]bool{"codex": true, "pi": true}
	for _, backend := range []string{"claude-code", "opencode"} {
		if LunaRouteBackendEnabled(backend) {
			t.Errorf("R7 defers LunaRoute on %s", backend)
		}
	}
	if len(lunaRouteEnabledBackends) != len(want) {
		t.Fatalf("shipped LunaRoute backends = %v, want %v", lunaRouteEnabledBackends, want)
	}
	for backend := range want {
		if !LunaRouteBackendEnabled(backend) {
			t.Fatalf("backend %q must be enabled; got %v", backend, lunaRouteEnabledBackends)
		}
	}
}
