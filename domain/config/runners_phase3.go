package config

import (
	"fmt"
	"path/filepath"
)

// Phase-3 (workspace mobility) defaults. The loader seeds these into the
// koanf defaults map; Validate rejects out-of-range values fail-fast.
const (
	DefaultMoveLoadThresholdPct       = 50
	DefaultMoveSizeCapBytes     int64 = 2 << 30 // 2 GiB
	DefaultRestartGraceSeconds        = 180
	DefaultTransferPartBytes    int64 = 64 << 20 // 64 MiB
	// MinTransferPartBytes / MaxTransferPartBytes bound one PUT body. The max
	// stays under Cloudflare's 100 MB Free/Pro request-body cap
	// (docs/evidence/remote-runners/research-transport.md §2).
	MinTransferPartBytes           int64 = 1 << 20
	MaxTransferPartBytes           int64 = 95 << 20
	DefaultStagingRetentionSeconds       = 3600
)

// validatePhase3 returns one error per violated Phase-3 bound. Validate joins
// them with the Phase-1/2 violations.
func (c RunnersConfig) validatePhase3() []error {
	var errs []error
	if c.MoveLoadThresholdPct < 1 || c.MoveLoadThresholdPct > 100 {
		errs = append(errs, fmt.Errorf("runners.moveloadthresholdpct must be 1..100 (got %d)", c.MoveLoadThresholdPct))
	}
	if c.MoveSizeCapBytes < 0 {
		errs = append(errs, fmt.Errorf("runners.movesizecapbytes must be >= 0 (got %d); 0 disables load moves", c.MoveSizeCapBytes))
	}
	if c.RestartGraceSeconds < 30 || c.RestartGraceSeconds > 3600 {
		errs = append(errs, fmt.Errorf("runners.restartgraceseconds must be 30..3600 (got %d)", c.RestartGraceSeconds))
	}
	if c.TransferPartBytes < MinTransferPartBytes || c.TransferPartBytes > MaxTransferPartBytes {
		errs = append(errs, fmt.Errorf("runners.transferpartbytes must be %d..%d (got %d)", MinTransferPartBytes, MaxTransferPartBytes, c.TransferPartBytes))
	}
	if c.StagingRetentionSeconds < 60 {
		errs = append(errs, fmt.Errorf("runners.stagingretentionseconds must be >= 60 (got %d)", c.StagingRetentionSeconds))
	}
	stagingSet, checkpointSet := c.StagingDir != "", c.CheckpointDir != ""
	if !stagingSet {
		errs = append(errs, fmt.Errorf("runners.stagingdir must not be empty"))
	}
	if !checkpointSet {
		errs = append(errs, fmt.Errorf("runners.checkpointdir must not be empty"))
	}
	if stagingSet && checkpointSet && filepath.Clean(c.StagingDir) == filepath.Clean(c.CheckpointDir) {
		errs = append(errs, fmt.Errorf("runners.stagingdir and runners.checkpointdir must differ (both %q)", c.StagingDir))
	}
	return errs
}
