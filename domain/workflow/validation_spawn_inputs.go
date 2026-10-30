package workflow

import (
	"errors"
	"strconv"
	"strings"
)

// validateSpawnChildInputs checks every spawn_workflow node's explicit inputs
// against the child's declared inputs. It runs only when a ChildContractReader
// is supplied. Each distinct child is loaded once per call. UNKNOWN_CHILD_WORKFLOW
// is reported here only when no name-only WorkflowChecker is present, so an
// unknown child is never reported twice.
//
// These findings never gate writes (IsGroupValidationCode is false for them).
func validateSpawnChildInputs(wf *Workflow, o validateOptions) []ValidationError {
	if o.children == nil {
		return nil
	}
	cache := map[string]childContract{}
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeSpawnWorkflow || n.Config.ChildWorkflow == "" {
			continue // MISSING_REQUIRED_CONFIG is reported by the config pass
		}
		child := n.Config.ChildWorkflow
		c, ok := cache[child]
		if !ok {
			fields, err := o.children.ChildInputs(child)
			c = childContract{fields: fields, err: err}
			cache[child] = c
		}
		switch {
		case c.err != nil && errors.Is(c.err, ErrNotFound):
			if o.workflows == nil {
				errs = append(errs, ValidationError{
					Code: "UNKNOWN_CHILD_WORKFLOW",
					Message: "spawn_workflow node " + n.ID + " references unknown workflow " +
						strconv.Quote(child),
					NodeID: n.ID,
				})
			}
		case c.err != nil:
			errs = append(errs, ValidationError{
				Code: "SPAWN_CHILD_CONTRACT_UNAVAILABLE",
				Message: "spawn_workflow node " + n.ID + ": could not load the input contract of child workflow " +
					strconv.Quote(child) + " (" + c.err.Error() + "); its inputs were not checked",
				NodeID:   n.ID,
				Severity: groupAdvisorySeverity,
			})
		default:
			errs = append(errs, checkSpawnNodeInputs(n, c.fields)...)
		}
	}
	return errs
}

func checkSpawnNodeInputs(n Node, fields []InputField) []ValidationError {
	child := n.Config.ChildWorkflow
	found := checkChildInputMap(fields, n.Config.Inputs)
	var errs []ValidationError
	for _, k := range found.unknown {
		errs = append(errs, ValidationError{
			Code: "SPAWN_UNKNOWN_CHILD_INPUT",
			Message: "spawn_workflow node " + n.ID + " sets input " + strconv.Quote(k) +
				", which child workflow " + strconv.Quote(child) + " does not declare (declared: " + declaredNames(fields) + ")",
			NodeID: n.ID,
		})
	}
	for _, m := range found.mismatches {
		errs = append(errs, ValidationError{
			Code: "SPAWN_INPUT_TYPE_MISMATCH",
			Message: "spawn_workflow node " + n.ID + " input " + strconv.Quote(m.key) + " " + m.problem +
				"; child workflow " + strconv.Quote(child) + " declares it as " + m.field.Type,
			NodeID: n.ID,
		})
	}

	var prepared []string
	for _, f := range found.unmappedRequired {
		if f.Default == nil {
			prepared = append(prepared, f.Name)
		}
	}
	if len(prepared) == 0 {
		return errs
	}
	list := strings.Join(prepared, ", ")
	if n.Config.ModelProfile == "" {
		errs = append(errs, ValidationError{
			Code: "SPAWN_PREPARER_PROFILE_MISSING",
			Message: "spawn_workflow node " + n.ID + " leaves required input(s) of child workflow " + strconv.Quote(child) +
				" unmapped (" + list + ") and has no model_profile to prepare them; map them in inputs or set model_profile",
			NodeID: n.ID,
		})
	}
	errs = append(errs, ValidationError{
		Code: "SPAWN_PREPARED_INPUTS",
		Message: "spawn_workflow node " + n.ID + " does not map required input(s) of child workflow " + strconv.Quote(child) +
			" (" + list + "); an LLM will prepare them at run time",
		NodeID:   n.ID,
		Severity: groupAdvisorySeverity,
	})
	return errs
}
