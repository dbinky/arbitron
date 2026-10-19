package engine

import (
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
)

// defaultGroupResolverPrompt is the conflict-resolution prompt used when a
// parallel_group sets no resolverPromptFile.
//
//go:embed assets/group_resolver_prompt.md
var defaultGroupResolverPrompt string

// ErrGroupResolverPrompt marks a failure to build the resolution prompt
// (missing/unreadable/empty override, or incomplete resolution data).
var ErrGroupResolverPrompt = errors.New("engine: group resolver prompt")

// GroupResolverPromptData is the persisted conflict data a resolution prompt
// is rendered with. The parent's variables are never substituted.
type GroupResolverPromptData struct {
	TaskKey       string   // task whose merge conflicted
	BaselineRef   string   // common starting revision, resolvable in WorkDir
	MergeRef      string   // finalized ref of the task being merged
	ConflictPaths []string // conflicted paths, relative to WorkDir
	WorkDir       string   // the staging directory the agent works in
	Attempt       int      // 1-based resolution attempt for this merge
}

// DefaultGroupResolverPrompt returns the embedded default prompt template.
func DefaultGroupResolverPrompt() string { return defaultGroupResolverPrompt }

// LoadGroupResolverPrompt renders the embedded default or a workflow-relative
// override loaded under the parent owner's scope, with includes expanded.
// Only resolution data is substituted; unknown variables stay literal.
func LoadGroupResolverPrompt(fr FileReader, owner uuid.UUID, workflowName, overridePath string, data GroupResolverPromptData) (string, error) {
	if strings.TrimSpace(data.TaskKey) == "" {
		return "", fmt.Errorf("%w: task key is required", ErrGroupResolverPrompt)
	}
	if strings.TrimSpace(data.WorkDir) == "" {
		return "", fmt.Errorf("%w: staging directory is required", ErrGroupResolverPrompt)
	}
	if len(data.ConflictPaths) == 0 {
		return "", fmt.Errorf("%w: at least one conflicted path is required", ErrGroupResolverPrompt)
	}

	text := defaultGroupResolverPrompt
	if path := strings.TrimSpace(overridePath); path != "" {
		if fr == nil {
			return "", fmt.Errorf("%w: resolverPromptFile %q is configured but no file reader is available", ErrGroupResolverPrompt, path)
		}
		loaded, err := loadTemplate(fr, owner, workflowName, path)
		if err != nil {
			return "", fmt.Errorf("%w: read resolverPromptFile %q: %w", ErrGroupResolverPrompt, path, err)
		}
		text = loaded
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%w: prompt is empty", ErrGroupResolverPrompt)
	}

	attempt := data.Attempt
	if attempt < 1 {
		attempt = 1
	}
	store := variable.NewStore()
	inputs := []struct {
		key   string
		value string
	}{
		{"taskKey", data.TaskKey},
		{"baselineRef", data.BaselineRef},
		{"mergeRef", data.MergeRef},
		{"conflictPaths", bulletList(data.ConflictPaths)},
		{"workDir", data.WorkDir},
		{"attempt", strconv.Itoa(attempt)},
	}
	for _, in := range inputs {
		if err := store.SetInput(in.key, in.value); err != nil {
			return "", fmt.Errorf("%w: %w", ErrGroupResolverPrompt, err)
		}
	}
	return variable.Resolve(text, store), nil
}

func bulletList(items []string) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, "- "+it)
	}
	return strings.Join(lines, "\n")
}
