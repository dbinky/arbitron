package codex

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

// ErrToolScopeUnsupported refuses a launch whose tool scope codex-cli cannot
// enforce (see the P1 T10 Cycle 6 evidence: apply_patch and view_image cannot
// be unregistered, reads go through the shell tool).
var ErrToolScopeUnsupported = errors.New("codex: tool scope cannot be enforced by codex-cli")

// ErrContextWindowUnsupported refuses a context tier with no codex emission.
var ErrContextWindowUnsupported = errors.New("codex: context window has no codex-cli emission")

// codexContextConfigKey is codex's ConfigToml context-window field.
const codexContextConfigKey = "model_context_window"

// toolPolicy is the codex launch posture derived from a non-empty tool scope.
type toolPolicy struct {
	Restricted bool     // --strict-config + per-server MCP disable
	ReadOnly   bool     // no write tool granted: read-only sandbox
	Overrides  []string // fixed-order "-c" pairs
}

var (
	codexWriteTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}
	codexReadTools  = map[string]bool{"Read": true, "Grep": true, "Glob": true, "LS": true}
	// No separate codex surface: Bash already governs this reach.
	codexInertTools = map[string]bool{"WebFetch": true, "Task": true, "TodoWrite": true}
)

func codexToolPolicy(scope []string) (toolPolicy, error) {
	if len(scope) == 0 {
		return toolPolicy{}, nil
	}
	var bash, write, search bool
	for _, raw := range scope {
		name := strings.TrimSpace(raw)
		switch {
		case name == "Bash":
			bash = true
		case name == "WebSearch":
			search = true
		case codexWriteTools[name]:
			write = true
		case codexReadTools[name], codexInertTools[name]:
		default:
			// Quote the exact profile entry (e.g. "Bash(gh:*)") so the operator
			// knows which allowed-tools item to change.
			return toolPolicy{}, fmt.Errorf("%w: tool %q has no codex-cli equivalent", ErrToolScopeUnsupported, name)
		}
	}
	if !bash {
		return toolPolicy{}, fmt.Errorf("%w: scope lacks %q: codex reads and searches only through its shell tool and cannot unregister view_image/apply_patch", ErrToolScopeUnsupported, "Bash")
	}
	p := toolPolicy{Restricted: true, ReadOnly: !write,
		Overrides: []string{"-c", "features.plugins=false", "-c", "features.apps=false"}}
	if !search {
		p.Overrides = append(p.Overrides, "-c", `web_search="disabled"`)
	}
	return p, nil
}

func codexContextArgs(b provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	tier := b.ContextWindow
	if tier == "" || tier == provider.ContextDefault {
		return nil, nil
	}
	if !rules.Empty() {
		backend := b.Backend
		if backend == "" {
			backend = codexBackend
		}
		effective, _, err := rules.Validate(backend, b.Model, tier, provider.EffortDefault)
		if err != nil {
			return nil, err
		}
		tier = effective
	}
	switch tier {
	case provider.ContextDefault:
		return nil, nil
	case provider.Context200K:
		return []string{"-c", codexContextConfigKey + "=200000"}, nil
	case provider.Context1M:
		return []string{"-c", codexContextConfigKey + "=1000000"}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrContextWindowUnsupported, tier)
}
