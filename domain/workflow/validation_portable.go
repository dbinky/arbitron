package workflow

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

var (
	ErrBundleOutsideReference = errors.New("script bundle references a path outside its folder")
	ErrBundleUnsupportedFile  = errors.New("script bundle contains an unsupported file")
	ErrBundleTooLarge         = errors.New("script bundle exceeds size limit")
)

// ScriptBundleChecker checks script folders in the trusted caller's file scope.
type ScriptBundleChecker interface {
	CheckBundleable(context.Context, uuid.UUID, string, string) error
}

// WithScriptBundleChecker binds the caller context and trusted file owner.
func WithScriptBundleChecker(ctx context.Context, owner uuid.UUID, c ScriptBundleChecker) ValidateOption {
	return func(o *validateOptions) {
		if c != nil {
			o.bundles, o.bundleCtx, o.bundleOwner = c, ctx, owner
		}
	}
}

func validatePortableNodes(wf *Workflow, o *validateOptions) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Config.Portable && n.Type != NodeTypeScript && n.Type != NodeTypeToolCall {
			errs = append(errs, ValidationError{Code: "INVALID_PORTABLE", NodeID: n.ID, Message: "node " + n.ID + " uses portable on a non-process node"})
		}
		if n.Config.Portable && n.Type == NodeTypeScript {
			if o.bundles == nil {
				errs = append(errs, ValidationError{Code: "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE", NodeID: n.ID, Message: "portable script node " + n.ID + " requires a script bundle checker"})
			} else if err := o.bundles.CheckBundleable(o.bundleCtx, o.bundleOwner, wf.Name, n.Config.ScriptFile); err != nil {
				code := "SCRIPT_BUNDLE_CHECK_FAILED"
				if errors.Is(err, ErrBundleOutsideReference) || errors.Is(err, ErrBundleUnsupportedFile) || errors.Is(err, ErrBundleTooLarge) {
					code = "SCRIPT_NOT_PORTABLE"
				}
				errs = append(errs, ValidationError{Code: code, NodeID: n.ID, Message: "portable script node " + n.ID + ": " + err.Error()})
			}
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
