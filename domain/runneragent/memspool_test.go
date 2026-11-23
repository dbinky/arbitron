package runneragent_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/arbitron/arbitron/domain/runneragent/spooltest"
)

func TestMemorySpoolConformance(t *testing.T) {
	spooltest.Run(t, func(t *testing.T, capacity int) runneragent.Spool {
		return runneragent.NewMemorySpool(capacity)
	})
}
