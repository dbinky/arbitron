package main

import "testing"

// SECURITY — the codex interactive provider gate DEFAULTS OFF: an unverified
// build (no opt-in env) must not be able to arm the SPIKE-unverified protocol
// against a live codex binary. Only an explicit truthy opt-in enables it.
func TestCodexInteractiveVerified(t *testing.T) {
	cases := []struct {
		name   string
		envVal string
		want   bool
	}{
		// DEFAULT OFF — the security-critical cases.
		{"unset/empty defaults off", "", false},
		{"whitespace defaults off", "   ", false},
		{"explicit false", "false", false},
		{"explicit 0", "0", false},
		{"no", "no", false},
		{"off", "off", false},
		{"garbage defaults off", "maybe", false},

		// Explicit opt-in (case-insensitive, trimmed).
		{"1 enables", "1", true},
		{"true enables", "true", true},
		{"TRUE enables", "TRUE", true},
		{"yes enables", "yes", true},
		{"on enables", "on", true},
		{"padded true enables", "  true  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexInteractiveVerified(tc.envVal); got != tc.want {
				t.Fatalf("codexInteractiveVerified(%q) = %v, want %v", tc.envVal, got, tc.want)
			}
		})
	}
}
