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

func TestLunaRouteEnabledBackends_Phase1ShipsNone(t *testing.T) {
	if len(lunaRouteEnabledBackends) != 0 {
		t.Fatalf("phase 1 enables no LunaRoute agent backend; got %v", lunaRouteEnabledBackends)
	}
	for _, backend := range LunaRouteBackends {
		if LunaRouteBackendEnabled(backend) {
			t.Fatalf("phase 1 enabled %s", backend)
		}
	}
}
