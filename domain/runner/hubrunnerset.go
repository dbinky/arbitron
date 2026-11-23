package runner

import "context"

// hubRunnerSet resolves built-in work locally and registered remote work via the hub.
type hubRunnerSet struct {
	builtin StepRunner
	remote  StepRunner
	reg     Registry
}

// NewHubRunnerSet composes the local and remote execution paths.
func NewHubRunnerSet(builtin StepRunner, remote StepRunner, reg Registry) RunnerSet {
	if builtin == nil || remote == nil || reg == nil {
		panic("runner: NewHubRunnerSet requires builtin, remote and registry")
	}
	return &hubRunnerSet{builtin: builtin, remote: remote, reg: reg}
}

func (s *hubRunnerSet) For(id RunnerID) (StepRunner, bool) {
	switch id {
	case "":
		return nil, false
	case BuiltInRunnerID:
		return s.builtin, true
	}
	r, _, err := s.reg.GetRunner(context.Background(), id)
	if err != nil || r.BuiltIn {
		return nil, false
	}
	return s.remote, true
}
