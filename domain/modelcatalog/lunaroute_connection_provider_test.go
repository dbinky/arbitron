package modelcatalog

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fakeCandidateInventory) ConnectionProvider(_ context.Context, p provider.Profile) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	for _, request := range f.reqs {
		if request.ConnectionRef == p.ConnectionRef {
			return request.RouteProvider, nil
		}
	}
	return "", nil
}

func TestLunaRouteConnectionMetadataSurvivesInventoryDecorator(t *testing.T) {
	inv := &fakeCandidateInventory{reqs: []modelidentity.VersionRequest{{ConnectionRef: "opaque", RouteProvider: "lunaroute"}}}
	// The same interface-embedding pattern is used by policy-race decorators.
	wrapped := struct{ CandidateInventory }{inv}
	svc := &CandidateService{d: CandidateDeps{Inventory: wrapped}}
	routing, err := svc.ConnectionProvider(context.Background(), provider.Profile{ConnectionRef: "opaque"})
	require.NoError(t, err)
	assert.Equal(t, "lunaroute", routing)
}
