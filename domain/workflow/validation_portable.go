package workflow

import (
	"strings"
	"unicode"
)

func validatePortableNodes(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Config.Portable && n.Type != NodeTypeScript && n.Type != NodeTypeToolCall {
			errs = append(errs, ValidationError{Code: "INVALID_PORTABLE", NodeID: n.ID, Message: "node " + n.ID + " uses portable on a non-process node"})
		}
		if n.Config.Portable && n.Type == NodeTypeScript {
			errs = append(errs, ValidationError{Code: "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE", NodeID: n.ID, Message: "portable script node " + n.ID + " requires a script bundle checker"})
		}
		seen := make(map[string]bool, len(n.Config.Requires))
		for _, name := range n.Config.Requires {
			if name == "" || strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsSpace) || seen[name] {
				errs = append(errs, ValidationError{Code: "INVALID_REQUIRES", NodeID: n.ID, Message: "node " + n.ID + " requires unique bare command names"})
				break
			}
			seen[name] = true
		}
		if len(n.Config.Requires) > 0 && !n.Config.Portable {
			errs = append(errs, ValidationError{Code: "REQUIRES_WITHOUT_PORTABLE", NodeID: n.ID, Message: "node " + n.ID + " declares requires without portable: true"})
		}
	}
	return errs
}
