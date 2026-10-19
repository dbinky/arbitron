package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ChildContractReader loads the declared inputs of a child workflow for
// author-time validation of workflows-form parallel_group tasks. It is the
// group analogue of WorkflowChecker, which answers existence only and cannot
// supply a contract.
//
// Implementations MUST be scoped to the author: resolution follows the same
// private-then-shared, visibility-filtered lookup the run will use, so a
// workflow the author cannot see is indistinguishable from one that does not
// exist.
type ChildContractReader interface {
	// ChildInputs returns the declared inputs of a workflow VISIBLE to the author.
	// Invisible/absent => error wrapping ErrNotFound. Any other error => lookup failure.
	ChildInputs(name string) ([]InputField, error)
}

// WithChildContractReader enables author-time checks of every workflows-form
// task against its child's declared inputs: unknown explicit input keys,
// statically known literal types, and an advisory for required inputs that
// are not explicitly mapped.
func WithChildContractReader(r ChildContractReader) ValidateOption {
	return func(o *validateOptions) { o.children = r }
}

// WithGroupCheckersRequired makes the group checkers mandatory: when the
// workflow has at least one parallel_group, an absent ChildContractReader,
// ProfileChecker or FileChecker is reported as GROUP_CHECKER_UNAVAILABLE
// instead of the corresponding check being silently skipped. Write paths
// (Service.Create/Update, the group operations, the validate endpoints) pass
// it; a workflow without groups is unaffected.
func WithGroupCheckersRequired() ValidateOption {
	return func(o *validateOptions) { o.groupCheckersRequired = true }
}

// groupAdvisorySeverity marks a non-gating group finding.
const groupAdvisorySeverity = "warning"

// childContract is one memoized ChildInputs answer.
type childContract struct {
	fields []InputField
	err    error
}

// validateGroupChildren checks every workflows-form task's child reference.
// With a ChildContractReader it loads each distinct child once per call and
// checks the task's explicit inputs; without one it falls back to the
// name-only WorkflowChecker (existence) when that is supplied. Without either,
// nothing is checked here (validateGroupCheckerAvailability reports that when
// checkers are required).
func validateGroupChildren(wf *Workflow, o validateOptions) []ValidationError {
	if o.children == nil && o.workflows == nil {
		return nil
	}
	cache := map[string]childContract{}
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup || n.Config.Group == nil || n.Config.Group.Form != GroupFormWorkflows {
			continue
		}
		for _, t := range n.Config.Group.Tasks {
			if t.Workflow == "" {
				continue // GROUP_INVALID_TASK_TARGET already reported by the config pass
			}
			if o.children == nil {
				if !o.workflows.WorkflowExists(t.Workflow) {
					errs = append(errs, unknownChildWorkflow(n.ID, t))
				}
				continue
			}
			c, ok := cache[t.Workflow]
			if !ok {
				fields, err := o.children.ChildInputs(t.Workflow)
				c = childContract{fields: fields, err: err}
				cache[t.Workflow] = c
			}
			switch {
			case c.err != nil && errors.Is(c.err, ErrNotFound):
				errs = append(errs, unknownChildWorkflow(n.ID, t))
			case c.err != nil:
				errs = append(errs, ValidationError{
					Code: "GROUP_CHECKER_UNAVAILABLE",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + ": could not load the input contract of child workflow " +
						strconv.Quote(t.Workflow) + " (" + c.err.Error() + "); retry validation once the workflow store is reachable",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
			default:
				errs = append(errs, checkTaskInputs(n.ID, t, c.fields)...)
			}
		}
	}
	return errs
}

func unknownChildWorkflow(groupID string, t GroupTask) ValidationError {
	return ValidationError{
		Code: "GROUP_UNKNOWN_CHILD_WORKFLOW",
		Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " references unknown workflow " +
			strconv.Quote(t.Workflow) + "; use the name of a workflow you can see (workflow_list)",
		NodeID:  groupID,
		TaskKey: t.Key,
	}
}

// checkTaskInputs checks one task's explicit inputs against its child's
// declared fields. Keys are visited in sorted order so findings are stable.
func checkTaskInputs(groupID string, t GroupTask, fields []InputField) []ValidationError {
	declared := make(map[string]InputField, len(fields))
	for _, f := range fields {
		declared[f.Name] = f
	}
	keys := make([]string, 0, len(t.Inputs))
	for k := range t.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []ValidationError
	for _, k := range keys {
		if k == groupReservedInputName {
			continue // GROUP_RESERVED_INPUT already reported by the config pass
		}
		f, ok := declared[k]
		if !ok {
			errs = append(errs, ValidationError{
				Code: "GROUP_UNKNOWN_CHILD_INPUT",
				Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " sets input " + strconv.Quote(k) +
					", which child workflow " + strconv.Quote(t.Workflow) + " does not declare (declared: " + declaredNames(fields) + ")",
				NodeID:  groupID,
				TaskKey: t.Key,
			})
			continue
		}
		if problem := groupLiteralTypeProblem(f, t.Inputs[k]); problem != "" {
			errs = append(errs, ValidationError{
				Code: "GROUP_INPUT_TYPE_MISMATCH",
				Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " input " + strconv.Quote(k) +
					" " + problem + "; child workflow " + strconv.Quote(t.Workflow) + " declares it as " + f.Type,
				NodeID:  groupID,
				TaskKey: t.Key,
			})
		}
	}

	for _, f := range fields {
		if !f.Required {
			continue
		}
		if _, mapped := t.Inputs[f.Name]; mapped {
			continue
		}
		errs = append(errs, ValidationError{
			Code: "GROUP_REQUIRED_INPUT_RUNTIME",
			Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " does not map required input " +
				strconv.Quote(f.Name) + " of child workflow " + strconv.Quote(t.Workflow) +
				"; it must be present in the group-start variables when the group runs (checked at run time), or map it explicitly in the task's inputs",
			NodeID:   groupID,
			TaskKey:  t.Key,
			Severity: groupAdvisorySeverity,
		})
	}
	return errs
}

func declaredNames(fields []InputField) string {
	if len(fields) == 0 {
		return "none"
	}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// groupLiteralTypeProblem returns "" when v is acceptable for field f, else a
// description of the mismatch. A WHOLE-value template ("{{path}}") resolves to
// a typed value at run time and is never checked. A null literal is accepted
// for an optional input (the child's default applies, exactly as at trigger
// time) and rejected for a required one. An unrecognized declared type is not
// checked (validateInputTypes owns that finding on the child).
func groupLiteralTypeProblem(f InputField, v any) string {
	if s, ok := v.(string); ok && isWholeTemplate(s) {
		return ""
	}
	if v == nil {
		if f.Required {
			return "is null but the input is required"
		}
		return ""
	}
	var ok bool
	switch f.Type {
	case "string":
		_, ok = v.(string)
	case "number":
		switch v.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			ok = true
		}
	case "boolean":
		_, ok = v.(bool)
	case "object":
		rv := reflect.ValueOf(v)
		ok = rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String
	case "array":
		k := reflect.ValueOf(v).Kind()
		ok = k == reflect.Slice || k == reflect.Array
	default:
		return ""
	}
	if ok {
		return ""
	}
	return "is a " + literalKind(v) + " literal"
}

// isWholeTemplate reports whether s is exactly one "{{path}}" reference.
func isWholeTemplate(s string) bool {
	if len(s) < 4 || !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return false
	}
	inner := s[2 : len(s)-2]
	return !strings.Contains(inner, "{{") && !strings.Contains(inner, "}}") && strings.TrimSpace(inner) != ""
}

func literalKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return "number"
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Map:
		return "object"
	case reflect.Slice, reflect.Array:
		return "array"
	}
	return reflect.TypeOf(v).String()
}

// validateGroupCheckerAvailability enforces WithGroupCheckersRequired: when
// the workflow has at least one parallel_group, each absent group checker —
// the child contract reader, the profile checker, the file checker — is one
// GROUP_CHECKER_UNAVAILABLE finding, attributed to the first group node. The
// rule is deliberately unconditional (not "only when this group needs it"): a
// write path that is missing a checker is mis-wired, and every group write is
// refused loudly until the composition root supplies all three.
func validateGroupCheckerAvailability(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError {
	if !o.groupCheckersRequired {
		return nil
	}
	var first *Node
	for i := range wf.Nodes {
		if wf.Nodes[i].Type == NodeTypeParallelGroup {
			first = &wf.Nodes[i]
			break
		}
	}
	if first == nil {
		return nil
	}
	var errs []ValidationError
	unavailable := func(what string) {
		errs = append(errs, ValidationError{
			Code: "GROUP_CHECKER_UNAVAILABLE",
			Message: "parallel_group definitions in this workflow cannot be fully validated: " + what +
				" is not configured on this server, so the workflow is rejected rather than accepted unverified (operator fix: wire the group checkers)",
			NodeID: first.ID,
		})
	}
	if o.children == nil {
		unavailable("the child workflow contract reader")
	}
	if profiles == nil {
		unavailable("the profile checker (resolverProfile)")
	}
	if files == nil {
		unavailable("the workflow file checker (resolverPromptFile)")
	}
	return errs
}
