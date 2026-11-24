package process

import (
	"context"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ScriptBundleChecker validates a portable script in its trusted owner's scope.
type ScriptBundleChecker struct{ files engine.FileReader }

func NewScriptBundleChecker(files engine.FileReader) *ScriptBundleChecker {
	if files == nil {
		panic("process: script bundle checker requires a file reader")
	}
	return &ScriptBundleChecker{files: files}
}

func (c *ScriptBundleChecker) CheckBundleable(ctx context.Context, owner uuid.UUID, workflowName, scriptFile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := c.files.ResolvePath(owner, workflowName, scriptFile)
	if err != nil {
		return err
	}
	return walkScriptBundle(ctx, resolved, nil)
}
