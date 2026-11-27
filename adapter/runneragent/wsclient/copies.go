package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func wireCopies(cs []runneragent.HeldCopy) []runnerproto.WireCopy {
	if len(cs) == 0 {
		return nil
	}
	out := make([]runnerproto.WireCopy, 0, len(cs))
	for _, c := range cs {
		out = append(out, runnerproto.WireCopy{RunID: c.RunID, HomeGeneration: c.HomeGeneration})
	}
	return out
}

func deleteCopyFromWire(d runnerproto.DeleteCopy) *runneragent.DeleteCopy {
	return &runneragent.DeleteCopy{RunID: d.RunID, HomeGeneration: d.HomeGeneration, Reason: d.Reason}
}
