package claudecli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// When the capability-env emission fails on the CHAT path (a non-empty ruleset
// with a backend it does not know), chatEnvWithBinding must WARN-log the backend
// and model before falling back to the bare base env — matching the agent path's
// failure visibility instead of swallowing the error silently. (Bridge until the
// resolver owns flooring and the emit step becomes total.)
func TestChatEnvWithBinding_WarnsOnEmitFailure(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A non-empty ruleset that does NOT contain the binding's backend forces
	// applyCapabilityEnv -> Emit -> Validate to return ErrUnknownBackend.
	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {Backend: "claude-code"},
	})
	core := adapterCore{rules: rules}
	binding := provider.ResolvedBinding{Backend: "ghost-backend", Model: "ghost-model"}

	env := core.chatEnvWithBinding(binding)

	// Still returns a usable base env (the chat turn is never blocked).
	if len(env) == 0 {
		t.Fatalf("expected a non-empty base env fallback, got %v", env)
	}
	out := buf.String()
	if !strings.Contains(strings.ToUpper(out), "WARN") {
		t.Fatalf("expected a WARN log on emit failure, got: %q", out)
	}
	if !strings.Contains(out, "ghost-backend") {
		t.Fatalf("expected the backend in the warn log, got: %q", out)
	}
	if !strings.Contains(out, "ghost-model") {
		t.Fatalf("expected the model in the warn log, got: %q", out)
	}
}

// On a clean binding (empty backend falls back to a known one) the chat env is
// built with NO warning.
func TestChatEnvWithBinding_QuietOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {Backend: "claude-code"},
	})
	core := adapterCore{rules: rules}

	_ = core.chatEnvWithBinding(provider.ResolvedBinding{}) // empty -> claudeBackend (known)

	if strings.Contains(strings.ToUpper(buf.String()), "WARN") {
		t.Fatalf("expected no WARN on a clean binding, got: %q", buf.String())
	}
}
