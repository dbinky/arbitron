package layout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReaderResultOutageUsesOnlyMatchingCacheFence(t *testing.T) {
	ctx := context.Background()
	m, req, f, best := readerRow(t)
	require.NoError(t, m.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, time.Now()))
	for _, kind := range []string{"matching", "old-job", "wrong-result", "non-outage"} {
		t.Run(kind, func(t *testing.T) {
			cache := layouttest.NewFakeCache()
			e := layout.CacheEntry{Format: layout.CacheFormatV1, Revision: req.Revision, Generation: 1, JobGeneration: 1, Contract: best.Geometry.Contract, Geometry: best.Geometry}
			err := error(layout.ErrStoreUnavailable)
			switch kind {
			case "old-job":
				e.JobGeneration = 2
			case "wrong-result":
				e.Generation = 2
			case "non-outage":
				err = errors.New("corrupt result")
			}
			cache.Seed(req.Scope, req.Name, e)
			v := layout.NewReader(readOnlyStore{corruptResultStore{Store: m, err: err}, t}, nil, cache).View(ctx, requestWorkflow(t, *req))
			assert.False(t, v.Available)
			assert.True(t, v.Unreviewed)
			assert.Nil(t, v.Evidence)
			if kind == "matching" {
				require.NotNil(t, v.Geometry)
				assert.Equal(t, best.Geometry, *v.Geometry)
				assert.Equal(t, layout.DisplayUnavailable, v.DisplayStatus)
			} else {
				assert.Nil(t, v.Geometry)
			}
		})
	}
}
