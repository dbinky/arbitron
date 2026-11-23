package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestWorkflowHandler_PortableValidationFromManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shared", "portable-wf")
	require.NoError(t, os.MkdirAll(dir, 0755))
	manifest := filepath.Join(dir, "workflow.json")
	h := adapthttp.NewWorkflowHandler(filesystem.NewWorkflowRepo(root), &mockWorkflowService{}, filesystem.NewFileReader(root), nil)
	for _, tc := range []struct{ requires, code string }{
		{`["jq","jq"]`, "INVALID_REQUIRES"},
		{`["jq","git"]`, ""},
	} {
		require.NoError(t, os.WriteFile(manifest, []byte(`{"name":"portable-wf","nodes":[{"id":"process","type":"tool_call","entrypoint":true,"config":{"command":"echo","portable":true,"requires":`+tc.requires+`}}]}`), 0644))
		req := withChiURLParam(httptest.NewRequest(http.MethodPost, "/workflows/portable-wf/validate", nil), "name", "portable-wf")
		w := httptest.NewRecorder()
		h.Validate(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Valid  bool                       `json:"valid"`
			Errors []workflow.ValidationError `json:"errors"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		if tc.code == "" {
			require.True(t, resp.Valid)
			require.Empty(t, resp.Errors)
		} else {
			require.False(t, resp.Valid)
			require.Len(t, resp.Errors, 1)
			require.Equal(t, tc.code, resp.Errors[0].Code)
			require.Equal(t, "process", resp.Errors[0].NodeID)
		}
	}
}
