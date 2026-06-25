package instance

import (
	"strings"

	"github.com/google/uuid"
)

const maxRunNameLen = 80

type RunNameInput struct {
	Name     string
	Required bool
}

func DeriveRunName(workflowName string, variables map[string]any, inputs []RunNameInput, id uuid.UUID) string {
	label := pickRunLabel(variables, inputs)
	sep := " — "
	budget := maxRunNameLen - len(workflowName) - len(sep)
	if workflowName == "" {
		budget = maxRunNameLen
	}
	label = truncate(label, budget)

	switch {
	case label != "" && workflowName != "":
		return label + sep + workflowName
	case label != "":
		return label
	case workflowName != "":
		return workflowName + " " + shortID(id)
	default:
		return shortID(id)
	}
}

func pickRunLabel(variables map[string]any, inputs []RunNameInput) string {
	for _, key := range []string{"title", "name", "slug"} {
		if s := stringVar(variables, key); s != "" {
			return s
		}
	}
	for _, inp := range inputs {
		if inp.Required {
			if s := stringVar(variables, inp.Name); s != "" {
				return s
			}
		}
	}
	for _, inp := range inputs {
		if s := stringVar(variables, inp.Name); s != "" {
			return s
		}
	}
	return ""
}

func stringVar(vars map[string]any, key string) string {
	if vars == nil {
		return ""
	}
	v, ok := vars[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func shortID(id uuid.UUID) string {
	s := id.String()
	if i := strings.Index(s, "-"); i > 0 {
		return s[:i]
	}
	return s
}
