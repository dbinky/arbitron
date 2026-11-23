package runner

import (
	"sort"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// HarnessPresence is what the host reports about one harness binary.
type HarnessPresence struct {
	Path      string
	Version   string
	Supported bool
	Reason    string // why unsupported; never provider prose
}

// InventoryInput is everything the built-in runner's source set derives from.
type InventoryInput struct {
	ClaudeCLI     *HarnessPresence      // nil = not found
	Codex         *HarnessPresence      // nil = not found
	Connections   []provider.Connection // metadata only; no key material
	LocalBackends []string              // "pi","opencode" when binaries found
}

// localHarnessBackends are the no-connection local harness paths (RR-02).
var localHarnessBackends = []string{"opencode", "pi"}

// BuiltInSources maps the inventory to the built-in runner's sources. Every
// subscription and local-harness source is ALWAYS registered (a missing or
// unsupported binary blocks eligibility via UnavailableReason), so unavailable harnesses remain visible while placement blocks them with a clear capacity reason.
// Initial health: auth unknown, capacity available.
func BuiltInSources(in InventoryInput) []Source {
	known := modelidentity.KnownFamilies()
	withUnresolved := append(append([]modelidentity.Family(nil), known...), modelidentity.FamilyUnresolved)
	var out []Source
	add := func(kind CredentialKind, ref string, fams []modelidentity.Family, backends []string, detail string) {
		out = append(out, Source{
			ID: MakeSourceID(BuiltInRunnerID, kind, ref), RunnerID: BuiltInRunnerID, Kind: kind, CredentialRef: ref,
			Families: fams, Backends: backends,
			UnavailableReason: detail,
			Health:            SourceHealth{Auth: AuthUnknown, Capacity: CapacityAvailable},
		})
	}
	add(CredAnthropicSubscription, "", []modelidentity.Family{"anthropic"}, []string{"", "claude-code"}, presenceDetail(in.ClaudeCLI))
	add(CredOpenAISubscription, "", []modelidentity.Family{"openai"}, []string{"codex"}, presenceDetail(in.Codex))
	for _, c := range in.Connections {
		if provider.IsLunaRoute(c.Provider) {
			add(CredLunaRoute, c.Name, append([]modelidentity.Family(nil), known...), []string{"", "codex", "pi"}, "")
			continue
		}
		add(CredServerConnection, c.Name, append([]modelidentity.Family(nil), withUnresolved...), []string{"", "claude-code", "codex", "opencode", "pi"}, "")
	}
	have := map[string]bool{}
	for _, b := range in.LocalBackends {
		have[b] = true
	}
	for _, b := range localHarnessBackends {
		detail := ""
		if !have[b] {
			detail = "harness_not_found"
		}
		add(CredLocalHarness, b, append([]modelidentity.Family(nil), withUnresolved...), []string{b}, detail)
	}
	add(CredNone, "", nil, nil, "")
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func presenceDetail(p *HarnessPresence) string {
	switch {
	case p == nil:
		return "harness_not_found"
	case !p.Supported:
		return "harness_unsupported"
	default:
		return ""
	}
}
