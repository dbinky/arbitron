package layout

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go arranger and dashboard must agree on every renderer value.
var contractJSONPath = filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "contract.json")

func TestDefaultContract_MatchesDashboardContractJSON(t *testing.T) {
	raw, err := os.ReadFile(contractJSONPath)
	require.NoError(t, err, "dashboard contract.json must exist")
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fromJSON RendererContract
	require.NoError(t, dec.Decode(&fromJSON))
	assert.Equal(t, DefaultContract(), fromJSON)
}

func TestPreviousContracts_MatchDashboardJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "contract.previous.json"))
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fromJSON []RendererContract
	require.NoError(t, dec.Decode(&fromJSON))
	assert.Equal(t, PreviousContracts(), fromJSON)
	for _, c := range fromJSON {
		assert.NotEqual(t, ContractVersion, c.Version, "a previous contract never shares the current version")
		assert.NoError(t, validateGeometryContract(c), "previous geometry must stay structurally valid")
	}
}

func TestDefaultContract_EveryFieldIsSet(t *testing.T) {
	v := reflect.ValueOf(DefaultContract())
	for i := 0; i < v.NumField(); i++ {
		assert.False(t, v.Field(i).IsZero(), "contract field %s must have a non-zero default", v.Type().Field(i).Name)
	}
}

func TestDefaultContract_PinnedValues(t *testing.T) {
	c := DefaultContract()
	assert.Equal(t, ContractVersion, c.Version)
	assert.Equal(t, "Inter, Noto Sans SC, Noto Emoji", c.FontFamily)
	assert.Equal(t, 0.9, c.PracticalZoom)
	assert.Equal(t, "gap", c.CrossingTreatment)
	assert.GreaterOrEqual(t, c.NodeTypeFontPx*c.PracticalZoom, c.MinReadableTextPx)
	assert.GreaterOrEqual(t, c.EdgeLabelFontPx*c.PracticalZoom, c.MinReadableTextPx)
	assert.GreaterOrEqual(t, c.NodeLabelFontPx*c.PracticalZoom, c.MinReadableTextPx)
}

// The contract radius is bounded by the crossing bend clearance preflight keeps.
// The bound alone does not keep a corner off a crossing: radius + gap half +
// bank exceeds that clearance, so the per-bend clamp in routedPath.ts
// cornerRadii (mirrored by the reviewer) is what keeps every gap on straight ink.
func TestDefaultContract_CornerRadiusFitsTheCrossingBendClearance(t *testing.T) {
	c := DefaultContract()
	assert.Equal(t, 6.0, c.CornerRadiusPx)
	assert.LessOrEqual(t, c.CornerRadiusPx, c.CrossingGapPx/2+c.StrokePx)
}

func TestValidateGeometryContract_RejectsCornerRadiusBeyondBendClearance(t *testing.T) {
	for _, r := range []float64{-1, math.NaN(), math.Inf(1), DefaultContract().CrossingGapPx/2 + DefaultContract().StrokePx + 0.5} {
		c := DefaultContract()
		c.CornerRadiusPx = r
		assert.ErrorIs(t, validateGeometryContract(c), ErrInvalidGeometry, "radius %v", r)
	}
	// 0 is sharp corners: geometry stored before the field existed stays valid
	// (still incompatible with the current contract, so it is re-arranged).
	for _, r := range []float64{0, DefaultContract().CrossingGapPx/2 + DefaultContract().StrokePx} {
		c := DefaultContract()
		c.CornerRadiusPx = r
		assert.NoError(t, validateGeometryContract(c), "radius %v", r)
	}
}
