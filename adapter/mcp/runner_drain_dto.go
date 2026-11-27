package mcp

import "github.com/arbitron/arbitron/domain/runner"

type drainBlockerJSON struct {
	Kind   string  `json:"kind"`
	RunID  *string `json:"runId"`
	Group  string  `json:"group"`
	Reason string  `json:"reason"`
}

type drainProgressJSON struct {
	ActiveSteps     int                `json:"activeSteps"`
	HomedWorkspaces int                `json:"homedWorkspaces"`
	Sessions        int                `json:"sessions"`
	Blocked         []drainBlockerJSON `json:"blocked"`
	Complete        bool               `json:"complete"`
}

func toDrainProgressJSON(p runner.DrainProgress) drainProgressJSON {
	out := drainProgressJSON{ActiveSteps: p.ActiveSteps, HomedWorkspaces: p.HomedWorkspaces, Sessions: p.Sessions,
		Complete: p.Complete, Blocked: make([]drainBlockerJSON, 0, len(p.Blocked))}
	for _, b := range p.Blocked {
		var run *string
		if b.RunID != nil {
			s := b.RunID.String()
			run = &s
		}
		out.Blocked = append(out.Blocked, drainBlockerJSON{Kind: b.Kind, RunID: run, Group: b.Group, Reason: b.Reason})
	}
	return out
}
