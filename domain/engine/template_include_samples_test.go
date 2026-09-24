package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

// commonMarker is the heading of samples/pr-review/_common.md. Asserting it is
// PRESENT (not merely that no directive is left over) proves the shared
// preamble actually reaches each persona prompt.
const commonMarker = "# Shared setup (read once per persona)"

// Every bundled sample prompt file must expand through the REAL filesystem
// reader — the same containment-checked adapter production wires — and every
// template that includes _common.md must come out carrying its content.
func TestSamplePromptFiles_ExpandThroughRealFileReader(t *testing.T) {
	for _, name := range []string{"pr-review", "feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			src := filepath.Join("..", "..", "samples", name)
			require.NoError(t, os.CopyFS(filepath.Join(base, "shared", name), os.DirFS(src)))
			fr := filesystem.NewFileReader(base)

			raw, err := os.ReadFile(filepath.Join(src, "workflow.json"))
			require.NoError(t, err)
			var wf workflow.Workflow
			require.NoError(t, json.Unmarshal(raw, &wf))

			for _, n := range wf.Nodes {
				for _, file := range []string{n.Config.TemplateFile, n.Config.OrchestrationFile} {
					if file == "" {
						continue
					}
					out, err := loadTemplate(fr, uuid.Nil, name, file)
					require.NoError(t, err, "node %q: %s must expand", n.ID, file)
					assert.NotContains(t, out, includeOpen, "node %q: %s left a directive unexpanded", n.ID, file)

					src, err := os.ReadFile(filepath.Join(base, "shared", name, file))
					require.NoError(t, err)
					if strings.Contains(string(src), "{{include:_common.md}}") {
						assert.Contains(t, out, commonMarker, "node %q: %s must carry _common.md", n.ID, file)
					}
				}
			}
		})
	}
}
