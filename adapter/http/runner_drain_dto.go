package http

import "github.com/arbitron/arbitron/domain/runner"

type drainBlockerResponse struct {
	Kind   string  `json:"kind"`
	RunID  *string `json:"runId"`
	Group  string  `json:"group"`
	Reason string  `json:"reason"`
}

type drainProgressResponse struct {
	ActiveSteps     int                    `json:"activeSteps"`
	HomedWorkspaces int                    `json:"homedWorkspaces"`
	Sessions        int                    `json:"sessions"`
	Blocked         []drainBlockerResponse `json:"blocked"`
	Complete        bool                   `json:"complete"`
}

func toDrainProgressResponse(p runner.DrainProgress) drainProgressResponse {
	out := drainProgressResponse{ActiveSteps: p.ActiveSteps, HomedWorkspaces: p.HomedWorkspaces, Sessions: p.Sessions,
		Complete: p.Complete, Blocked: make([]drainBlockerResponse, 0, len(p.Blocked))}
	for _, b := range p.Blocked {
		var run *string
		if b.RunID != nil {
			s := b.RunID.String()
			run = &s
		}
		out.Blocked = append(out.Blocked, drainBlockerResponse{Kind: b.Kind, RunID: run, Group: b.Group, Reason: b.Reason})
	}
	return out
}
