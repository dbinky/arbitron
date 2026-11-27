package http

import (
	"fmt"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

func toWireDeleteCopy(p *runner.DeleteCopyPayload) runnerproto.DeleteCopy {
	return runnerproto.DeleteCopy{RunID: p.RunID.String(), HomeGeneration: p.HomeGeneration, Reason: p.Reason}
}

func fromWireCopies(cs []runnerproto.WireCopy) ([]runner.HeldCopyInfo, error) {
	out := make([]runner.HeldCopyInfo, 0, len(cs))
	for _, c := range cs {
		id, err := uuid.Parse(c.RunID)
		if err != nil {
			return nil, fmt.Errorf("hello copy run id %q: %w", c.RunID, err)
		}
		out = append(out, runner.HeldCopyInfo{RunID: id, HomeGeneration: c.HomeGeneration})
	}
	return out, nil
}
