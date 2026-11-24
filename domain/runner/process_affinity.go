package runner

import "github.com/arbitron/arbitron/domain/engine"

// ProcessAffinity keeps non-portable processes local. Portable scripts also
// need a bundle so no server filesystem path is used as a remote script source.
func ProcessAffinity(spec engine.ProcessSpec) Affinity {
	if !spec.Portable || spec.WorkDir == "" {
		return AffinityBuiltIn
	}
	switch spec.Kind {
	case engine.ProcessScript:
		if spec.Bundle != nil {
			return AffinityWorkspace
		}
	case engine.ProcessToolCall:
		return AffinityWorkspace
	}
	return AffinityBuiltIn
}
