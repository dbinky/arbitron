package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// EffectiveBackend is the single domain rule for the write-time/seed-time backend
// a profile validates against. It must be byte-identical to the literal the
// ProfileService.write and SeedProfiles paths previously inlined: an explicit
// Backend wins; an empty Backend with no ConnectionRef defaults to the
// claude-cli no-key subscription path; an empty Backend WITH a ConnectionRef is
// left empty (the connection supplies the routing).
func TestEffectiveBackend(t *testing.T) {
	tests := []struct {
		name    string
		profile provider.Profile
		want    string
	}{
		{
			name:    "explicit backend wins",
			profile: provider.Profile{Backend: "codex", ConnectionRef: "c"},
			want:    "codex",
		},
		{
			name:    "explicit backend wins even without a connection",
			profile: provider.Profile{Backend: "claude-code"},
			want:    "claude-code",
		},
		{
			name:    "empty backend + empty connection defaults to claude-cli",
			profile: provider.Profile{},
			want:    "claude-cli",
		},
		{
			name:    "empty backend WITH a connection stays empty (connection supplies routing)",
			profile: provider.Profile{ConnectionRef: "openrouter-prod"},
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.EffectiveBackend(tc.profile); got != tc.want {
				t.Fatalf("EffectiveBackend(%+v) = %q, want %q", tc.profile, got, tc.want)
			}
		})
	}
}
