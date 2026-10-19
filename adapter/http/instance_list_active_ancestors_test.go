package http_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

func TestInstanceListREST_ActiveDescendantAncestorsParam(t *testing.T) {
	t.Run("true sets the narrowing", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "?activeDescendantAncestors=true")
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, got.ActiveDescendantAncestors)
	})
	t.Run("composes with the roots selector", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "?parent=roots&activeDescendantAncestors=1")
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, got.ActiveDescendantAncestors)
		assert.Equal(t, instance.ParentScopeRoots, got.ParentScope)
	})
	t.Run("absent leaves the list unchanged", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "")
		assert.Equal(t, http.StatusOK, code)
		assert.False(t, got.ActiveDescendantAncestors)
	})
	for _, v := range []string{"false", "0"} {
		t.Run(v+" clears the narrowing", func(t *testing.T) {
			h, got := newParentListHandler(nil)
			code, _ := doList(t, h, "?activeDescendantAncestors="+v)
			assert.Equal(t, http.StatusOK, code)
			assert.False(t, got.ActiveDescendantAncestors)
		})
	}
	for _, v := range []string{"", "TRUE", "T", "t", "False", "yes", "2"} {
		t.Run("reject "+v, func(t *testing.T) {
			h, _ := newParentListHandler(nil)
			w := adminListRequest(h, "?activeDescendantAncestors="+v)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "INVALID_FILTER")
		})
	}
	t.Run("malformed is rejected rather than ignored", func(t *testing.T) {
		h, _ := newParentListHandler(nil)
		w := adminListRequest(h, "?activeDescendantAncestors=maybe")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "INVALID_FILTER")
	})
}
