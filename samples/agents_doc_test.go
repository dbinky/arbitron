package samples_test

import (
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

// AGENTS.md is the orientation doc agents read first; its backend vocabulary
// must track the constants in domain/workflow.
func TestAgentsDocBackendVocabulary(t *testing.T) {
	raw, err := os.ReadFile("../AGENTS.md")
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	doc := string(raw)
	for _, b := range []string{
		workflow.BackendClaudeCode,
		workflow.BackendOpencode,
		workflow.BackendCodex,
		workflow.BackendPi,
	} {
		if !strings.Contains(doc, "`"+b+"`") {
			t.Errorf("AGENTS.md does not list backend %q in backticks", b)
		}
	}
}
