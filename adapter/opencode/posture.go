package opencode

import "github.com/arbitron/arbitron/domain/provider"

// Posture is the resolved tool/permission stance for one opencode run. It
// mirrors the claudecli agent posture (--dangerously-skip-permissions +
// --allowedTools) so both runners express the SAME open-by-default contract:
//
//   - AllowAll true, AllowedTools nil  => allow-all permissions/tools ON
//     (the friction-free default for the experimentation harness).
//   - AllowAll false, AllowedTools set => only the named tools are permitted
//     (per-profile narrowing, the operator's knob (b)).
//   - AllowAll false, AllowedTools nil => allow-all disabled by config and no
//     per-profile allowlist (the most restrictive posture the operator can
//     reach without a profile change, knob (a)).
type Posture struct {
	// AllowAll grants every tool/permission for the run when true.
	AllowAll bool
	// AllowedTools, when non-empty, is the explicit tool allowlist that narrows
	// the run regardless of AllowAll.
	AllowedTools []string
}

// buildPosture resolves the run posture from the binding's per-profile
// AllowedTools (knob (b)) and the config-level allow-all default (knob (a)).
//
// A non-empty binding.AllowedTools always wins: it disables allow-all and pins
// the run to the named tools. Otherwise the config default decides whether the
// run is allow-all.
func buildPosture(binding provider.ResolvedBinding, allowAllDefault bool) Posture {
	if len(binding.AllowedTools) > 0 {
		return Posture{AllowAll: false, AllowedTools: binding.AllowedTools}
	}
	return Posture{AllowAll: allowAllDefault}
}
