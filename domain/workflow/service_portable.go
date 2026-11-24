package workflow

import (
	"context"
	"fmt"
	"github.com/google/uuid"
)

func hasPortableConfig(wf *Workflow) bool {
	if wf != nil {
		for _, n := range wf.Nodes {
			if n.Config.Portable || len(n.Config.Requires) > 0 {
				return true
			}
		}
	}
	return false
}

func (s *Service) rejectUnportableScripts(ctx context.Context, wf *Workflow, owner uuid.UUID) error {
	if !hasPortableConfig(wf) {
		return nil
	}
	for _, finding := range Validate(wf, nil, nil, nil, WithScriptBundleChecker(ctx, owner, s.bundles)) {
		switch finding.Code {
		case "SCRIPT_NOT_PORTABLE", "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE", "SCRIPT_BUNDLE_CHECK_FAILED", "INVALID_PORTABLE", "INVALID_REQUIRES", "REQUIRES_WITHOUT_PORTABLE":
			return fmt.Errorf("%s (node %s): %s", finding.Code, finding.NodeID, finding.Message)
		}
	}
	return nil
}

// SetScriptBundleChecker installs the owner-aware checker used for validation
// and workflow writes. Without it portable scripts fail closed.
func (s *Service) SetScriptBundleChecker(c ScriptBundleChecker) { s.bundles = c }
