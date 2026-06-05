package probe

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/claudecli"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// claudeCLIProbe checks reachability of the local Claude subscription binary
// (no key, no listing endpoint). It runs `claude --version` through the
// CommandRunner seam and reports Reachable + ModelsUnsupported.
type claudeCLIProbe struct {
	runner claudecli.CommandRunner
	bin    string
}

func newClaudeCLIProbe(d Deps) *claudeCLIProbe {
	r := d.Runner
	if r == nil {
		r = claudecli.NewExecRunner()
	}
	bin := d.ClaudeBinaryPath
	if bin == "" {
		bin = "claude"
	}
	return &claudeCLIProbe{runner: r, bin: bin}
}

var _ domprovider.ConnectionProbe = (*claudeCLIProbe)(nil)

func (p *claudeCLIProbe) Probe(ctx context.Context, _ domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	lines, wait, err := p.runner.Run(ctx, claudecli.RunSpec{Bin: p.bin, Args: []string{"--version"}})
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	// Contract: drain lines to completion before calling wait (runner.go).
	for range lines {
	}
	if werr := wait(); werr != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, werr)
	}
	return domprovider.ProbeResult{Reachable: true, ModelsUnsupported: true}, nil
}
