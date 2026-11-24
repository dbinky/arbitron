package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/workflow"
)

func TestValidate_TimeoutRetryScale(t *testing.T) {
	for _, tc := range []struct {
		scale   int
		invalid bool
	}{
		{scale: 0}, {scale: 1}, {scale: 2}, {scale: 4},
		{scale: -1, invalid: true},
	} {
		wf := minimalValidWorkflow()
		wf.Nodes[0].Config.TimeoutRetryScale = tc.scale
		codes := extractCodes(workflow.Validate(wf, nil, nil, nil))
		if tc.invalid {
			assert.Contains(t, codes, "INVALID_TIMEOUT_RETRY_SCALE", "scale=%d", tc.scale)
		} else {
			assert.NotContains(t, codes, "INVALID_TIMEOUT_RETRY_SCALE", "scale=%d", tc.scale)
		}
	}
}
