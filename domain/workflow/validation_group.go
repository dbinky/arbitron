package workflow

import (
	"errors"
	"strconv"
	"strings"
)

// IsGroupValidationCode reports whether code is a parallel_group finding.
// Every group code — errors and the GROUP_REQUIRED_INPUT_RUNTIME advisory —
// carries the "GROUP_" prefix, so a caller can isolate group findings without
// enumerating them.
func IsGroupValidationCode(code string) bool {
	return strings.HasPrefix(code, "GROUP_")
}

// groupConfigCodes maps each ParseGroupConfig sentinel to its stable code.
var groupConfigCodes = []struct {
	sentinel error
	code     string
}{
	{ErrGroupConfigMissing, "GROUP_CONFIG_MISSING"},
	{ErrGroupFormInvalid, "GROUP_INVALID_FORM"},
	{ErrGroupNoTasks, "GROUP_NO_TASKS"},
	{ErrGroupTooManyTasks, "GROUP_TOO_MANY_TASKS"},
	{ErrGroupTaskKeyInvalid, "GROUP_INVALID_TASK_KEY"},
	{ErrGroupTaskKeyDuplicate, "GROUP_DUPLICATE_TASK_KEY"},
	{ErrGroupTaskTargetInvalid, "GROUP_INVALID_TASK_TARGET"},
	{ErrGroupTaskEntryDuplicate, "GROUP_DUPLICATE_ENTRY"},
	{ErrGroupReservedInput, "GROUP_RESERVED_INPUT"},
	{ErrGroupRuleInvalid, "GROUP_INVALID_RULE"},
	{ErrGroupUnfinishedInvalid, "GROUP_INVALID_UNFINISHED"},
	{ErrGroupGatherInvalid, "GROUP_INVALID_GATHER"},
	{ErrGroupResolverProfileMissing, "GROUP_RESOLVER_PROFILE_REQUIRED"},
}

// validateGroups runs every parallel_group check. It is a no-op for a
// workflow without groups, so legacy validation output is unchanged.
//
// Per group node: config problems (ParseGroupConfig, the parser the executor
// also uses), the static repoPath check, the resolverProfile kind check (when
// a ProfileChecker is supplied), the resolverPromptFile path/existence check
// (existence only when a FileChecker is supplied) and the direct self-run
// check. Then the shared ownership analysis (AnalyzeGroups) reports ports,
// task edges, body ownership and the cycle rule for the whole graph. Finally,
// child contracts and mandatory checker availability are validated.
func validateGroups(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError {
	if !HasGroups(wf) {
		return nil
	}
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup {
			continue
		}
		errs = append(errs, validateGroupNode(wf, n, files, profiles)...)
	}
	_, graphErrs := AnalyzeGroups(wf)
	errs = append(errs, graphErrs...)
	errs = append(errs, validateGroupChildren(wf, o)...)
	errs = append(errs, validateGroupCheckerAvailability(wf, files, profiles, o)...)
	return errs
}

// validateGroupNode checks one group node's configuration.
func validateGroupNode(wf *Workflow, n Node, files FileChecker, profiles ProfileChecker) []ValidationError {
	var errs []ValidationError

	_, parseErr := ParseGroupConfig(n.Config)
	for _, p := range GroupConfigProblems(parseErr) {
		code := "GROUP_CONFIG_MISSING"
		for _, m := range groupConfigCodes {
			if errors.Is(p, m.sentinel) {
				code = m.code
				break
			}
		}
		errs = append(errs, ValidationError{
			Code:    code,
			Message: "parallel_group " + n.ID + ": " + p.Error(),
			NodeID:  n.ID,
			TaskKey: p.TaskKey,
		})
	}

	raw := n.Config.Group
	if raw == nil {
		return errs // GROUP_CONFIG_MISSING already says everything actionable
	}

	if problem := groupRelativePathProblem(raw.RepoPath, true); problem != "" {
		errs = append(errs, ValidationError{
			Code: "GROUP_INVALID_REPO_PATH",
			Message: "parallel_group " + n.ID + " repoPath " + strconv.Quote(raw.RepoPath) + " " + problem +
				"; use a path relative to the parent's {{workspace}}, e.g. \"repo\" or \"{{repo_dir}}/app\"",
			NodeID: n.ID,
		})
	}

	// The resolver's required kind comes from the shared ModelConsumers
	// description, the same one run pin capture and the path inventory use.
	if c, ok := groupResolverConsumer(n); ok && profiles != nil {
		kind, ok := profiles.ProfileKind(c.Alias)
		switch {
		case !ok:
			errs = append(errs, ValidationError{
				Code:    "GROUP_UNKNOWN_RESOLVER_PROFILE",
				Message: "parallel_group " + n.ID + " references unknown resolverProfile " + strconv.Quote(c.Alias) + "; create the profile or use an existing agent profile alias",
				NodeID:  n.ID,
			})
		case kind != c.Kind:
			errs = append(errs, ValidationError{
				Code:    "GROUP_RESOLVER_PROFILE_KIND",
				Message: "parallel_group " + n.ID + " resolverProfile " + strconv.Quote(c.Alias) + " is a " + kind + " profile; conflict resolution edits files and requires an agent profile",
				NodeID:  n.ID,
			})
		}
	}

	if raw.ResolverPromptFile != "" {
		if problem := groupRelativePathProblem(raw.ResolverPromptFile, false); problem != "" {
			errs = append(errs, ValidationError{
				Code:    "GROUP_MISSING_PROMPT_FILE",
				Message: "parallel_group " + n.ID + " resolverPromptFile " + strconv.Quote(raw.ResolverPromptFile) + " " + problem + "; use a path relative to the workflow folder",
				NodeID:  n.ID,
			})
		} else if files != nil && !files.FileExists(wf.Name, raw.ResolverPromptFile) {
			errs = append(errs, ValidationError{
				Code:    "GROUP_MISSING_PROMPT_FILE",
				Message: "parallel_group " + n.ID + " references missing resolverPromptFile " + strconv.Quote(raw.ResolverPromptFile) + "; write it with workflow_write_file or remove the override to use the default prompt",
				NodeID:  n.ID,
			})
		}
	}

	if raw.Form == GroupFormWorkflows && wf.Name != "" {
		for _, t := range raw.Tasks {
			if t.Workflow == wf.Name {
				errs = append(errs, ValidationError{
					Code: "GROUP_INVALID_TASK_TARGET",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + " runs its own workflow " + strconv.Quote(wf.Name) +
						"; a run that starts itself recurses until it hits the runtime spawn-depth limit",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
			}
		}
	}

	return errs
}

// groupRelativePathProblem returns "" when p is statically acceptable as a
// path relative to a base directory, else a short description of the problem.
// Rejected: blank, absolute ("/..."), Windows drive-qualified paths, and any ".." segment — including after
// a template, since "{{x}}/../y" is an escape attempt whatever x resolves to.
// When allowTemplates, {{...}} segments are accepted as-is: their runtime
// value is checked for containment by the launcher (Phase 2/3), not here.
// When !allowTemplates, a template marker is itself a problem.
func groupRelativePathProblem(p string, allowTemplates bool) string {
	if p == "" {
		return ""
	}
	if strings.TrimSpace(p) == "" {
		return "is blank"
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return "is absolute"
	}
	// Check Windows volumes on every host: filepath.VolumeName is host-specific,
	// and even C:repo is relative to a drive's current directory, not our base.
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return "must not specify a Windows drive"
	}
	if !allowTemplates && strings.Contains(p, "{{") {
		return "must not contain templates"
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "must not contain \"..\" segments"
		}
	}
	return ""
}
