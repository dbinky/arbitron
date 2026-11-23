package config

import "fmt"

// validatePhase4 checks the interactive-session keys. Each violation names its
// loader key so an operator can find it in config.yaml or the ARBITRON_ env.
func (c RunnersConfig) validatePhase4() []error {
	var errs []error
	if c.TurnLeaseSeconds < 15 || c.TurnLeaseSeconds > 3600 {
		errs = append(errs, fmt.Errorf("runners.turnleaseseconds must be 15..3600, got %d", c.TurnLeaseSeconds))
	}
	if c.SessionBundleRetention < 1 || c.SessionBundleRetention > 16 {
		errs = append(errs, fmt.Errorf("runners.sessionbundleretention must be 1..16, got %d", c.SessionBundleRetention))
	}
	if c.RebuildMaxAttempts < 1 || c.RebuildMaxAttempts > 5 {
		errs = append(errs, fmt.Errorf("runners.rebuildmaxattempts must be 1..5, got %d", c.RebuildMaxAttempts))
	}
	return errs
}
