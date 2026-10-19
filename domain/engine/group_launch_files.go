package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// SetFileWorkingArea wires the working area that snapshots, forks, finalizes
// and gathers results_and_files groups (the git area, P3-T2/T3). Results-only
// groups keep the launcher's groupWS, so they never depend on git. Composition
// time only (before FreezeExecutors); a nil area refuses every file group.
func (l *GroupLauncher) SetFileWorkingArea(area WorkingArea) {
	l.fileArea = area
}

// launchWorkspace picks the snapshot/fork capability for this launch and, for
// file gathering, runs every check that must happen BEFORE a task exists: a
// file area is wired, the parent has a working area, the optional repoPath
// resolves inside it, and the area's own preflight (git availability, the
// declared repository) passes. It returns the resolved hint ("" = none).
func (l *GroupLauncher) launchWorkspace(ctx context.Context, req GroupLaunchRequest, parentWS string) (GroupWorkspace, string, error) {
	if req.Config.GatherMode != workflow.GroupGatherResultsAndFiles {
		return l.groupWS, "", nil
	}
	if l.fileArea == nil {
		return nil, "", fmt.Errorf("gatherMode %q needs a file-gathering working area, and none is wired on this server; use gatherMode %q",
			workflow.GroupGatherResultsAndFiles, workflow.GroupGatherResults)
	}
	if strings.TrimSpace(parentWS) == "" {
		return nil, "", fmt.Errorf("gatherMode %q gathers files into the parent run's working area ({{workspace}}), and this run has none",
			workflow.GroupGatherResultsAndFiles)
	}
	repoRel, err := ResolveGroupRepoPath(req.Config.RepoPath, req.Vars, parentWS)
	if err != nil {
		return nil, "", err
	}
	if p, ok := l.fileArea.(GatherPreflighter); ok {
		if err := p.PreflightGather(ctx, parentWS, repoRel); err != nil {
			return nil, "", fmt.Errorf("file-gathering preflight: %w", err)
		}
	}
	return l.fileArea, repoRel, nil
}
