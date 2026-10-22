package workflow

import (
	"fmt"
	"strings"
)

// DestinationError confines a durable-recovery failure to the destinations and
// record it names. It is classified where the failure is detected, so callers
// can keep unrelated destinations serving: every named destination stays
// guarded by Settle under its own lock until recovery succeeds. Errors that are
// not DestinationErrors are systemic (the journal itself cannot be read).
type DestinationError struct {
	// Targets lists the affected destinations when they are known. An intent
	// whose body is unreadable is identified only by Record.
	Targets []MutationTarget
	// Record is the relocation intent ID or journal revision at fault, if any.
	Record string
	Err    error
}

func (e *DestinationError) Error() string {
	var b strings.Builder
	b.WriteString("workflow recovery")
	for i, t := range e.Targets {
		sep := ", "
		if i == 0 {
			sep = " at "
		}
		fmt.Fprintf(&b, "%s%s/%s", sep, scopeIdentity(t.Scope), t.Name)
	}
	if e.Record != "" {
		fmt.Fprintf(&b, " (record %s)", e.Record)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *DestinationError) Unwrap() error { return e.Err }

func scopeIdentity(s Scope) string {
	if s.Kind == ScopePrivate {
		return ScopeLabel(s) + ":" + s.OwnerID.String()
	}
	return ScopeLabel(s)
}
