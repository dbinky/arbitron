package http

import (
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

func TestMapEngineError_Forbidden_403(t *testing.T) {
	assert.Equal(t, http.StatusForbidden, mapEngineError(instance.ErrForbidden))
}

func TestMapEngineErrorCode_Forbidden(t *testing.T) {
	assert.Equal(t, "FORBIDDEN", mapEngineErrorCode(instance.ErrForbidden))
}

// Not-found still maps to 404 (existence-hiding posture preserved).
func TestMapEngineError_NotFound_Still404(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, mapEngineError(instance.ErrNotFound))
}
