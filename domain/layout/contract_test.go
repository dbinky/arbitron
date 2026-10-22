package layout

import (
	"bytes"
	"encoding/json"
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
