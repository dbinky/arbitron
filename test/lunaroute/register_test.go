package lunaroute_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureRegister(t *testing.T) (string, register) {
	t.Helper()
	root := t.TempDir()
	put := func(path, text string) {
		t.Helper()
		p := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0700))
		require.NoError(t, os.WriteFile(p, []byte(text), 0600))
	}
	put(decisionPath, "# Decisions\nLR-01 LR-02 LR-03 LR-04 LR-05 LR-06 LR-07 LR-08\n")
	put(evidencePath+"/source.md", "# Source\nRoot: "+provider.LunaRouteHostedRoot+"\n")
	put("domain/runtime.go", "const HostedRoot = \""+provider.LunaRouteHostedRoot+"\"\n")
	put(evidencePath+"/observations/run.json", `{"host":"https://gw.lunaroute.com","terminal":true}`)
	r := register{Items: map[string]registerItem{}}
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("LR-%02d", i)
		r.Items[id] = registerItem{Title: "Contract", Status: "decided", Decision: decisionPath + " " + id}
	}
	return root, r
}

func TestRegisterSchema(t *testing.T) {
	root, r := fixtureRegister(t)
	assert.Empty(t, validateRegisterAt(root, r))
	assert.NotEmpty(t, releaseProblems(root, r), "valid pending is not release-ready")
	// Exercise the real checked-in input, not just the fixture.
	assert.Empty(t, validateRegister(loadRegister(t)))
}

func TestRegisterDeferredScope(t *testing.T) {
	root, r := fixtureRegister(t)
	for id, it := range r.Items {
		it.Status = "verified"
		it.Evidence = []string{evidencePath + "/source.md"}
		it.AppliedTo = []string{"domain/runtime.go:HostedRoot"}
		r.Items[id] = it
	}
	for _, id := range []string{"LR-05", "LR-06"} {
		it := r.Items[id]
		it.Status, it.Evidence, it.AppliedTo = "deferred", nil, nil
		r.Items[id] = it
	}
	require.Empty(t, validateRegisterAt(root, r))
	require.Empty(t, releaseProblems(root, r), "R7 excludes only the deferred backends")
	for _, id := range []string{"LR-01", "LR-02", "LR-03", "LR-04", "LR-07", "LR-08"} {
		it := r.Items[id]
		it.Status = "deferred"
		r.Items[id] = it
		require.NotEmpty(t, releaseProblems(root, r), id+" remains a release requirement")
		it.Status = "verified"
		r.Items[id] = it
	}
	for _, decision := range []string{"", "https://example.org/decision", "missing.md LR-05"} {
		it := r.Items["LR-05"]
		it.Decision = decision
		r.Items["LR-05"] = it
		require.NotEmpty(t, validateRegisterAt(root, r), "deferred still needs a readable local decision")
	}
}

func TestRegisterR7OperatorEvidence(t *testing.T) {
	r := loadRegister(t)
	for _, id := range []string{"LR-05", "LR-06"} {
		assert.Equal(t, "deferred", r.Items[id].Status)
		assert.Contains(t, r.Items[id].Title, "R7")
	}
	for id, control := range map[string]string{"LR-07": "lr07-codex-config-controls.json", "LR-08": "lr08-pi-model-controls.json"} {
		assert.Contains(t, r.Items[id].Evidence, evidencePath+"/observations/"+control)
		assert.Contains(t, r.Items[id].Evidence, evidencePath+"/observations/operator-manual-agent-tasks-2026-11-12.md")
		if len(r.Items[id].AppliedTo) == 0 {
			assert.NotEqual(t, "verified", r.Items[id].Status, "observations without application do not verify a contract")
		}
	}
	require.Empty(t, validateRegister(r))
}

func TestRegisterManualOperatorNoteBoundary(t *testing.T) {
	root, r := fixtureRegister(t)
	it := r.Items["LR-07"]
	ref := evidencePath + "/observations/operator-manual-agent-tasks-2026-11-12.md"
	it.Evidence = []string{ref}
	r.Items["LR-07"] = it
	path := filepath.Join(root, ref)
	require.NoError(t, os.WriteFile(path, []byte("# Operator manual tasks\nCodex and pi completed a file write.\n"), 0600))
	require.Empty(t, validateRegisterAt(root, r))
	for _, body := range []string{"", "# Operator\nlr_private-secret", `# Operator\n\u006c\u0072_private-secret`, "# Operator\nBearer foreign-secret"} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0600))
		problems := validateRegisterAt(root, r)
		require.NotEmpty(t, problems)
		assert.NotContains(t, strings.Join(problems, " "), "private-secret")
	}
	other := evidencePath + "/observations/unreviewed.md"
	require.NoError(t, os.WriteFile(filepath.Join(root, other), []byte("# Unreviewed"), 0600))
	it.Evidence = []string{other}
	r.Items["LR-07"] = it
	require.NotEmpty(t, validateRegisterAt(root, r), "only the R7 operator note is Markdown observation evidence")
}

// A credential-only scan must not let a reintroduced prompt make the released
// register ready. Start with the actual operator record and retain its results.
func TestReleaseGateRejectsManualPromptPayloads(t *testing.T) {
	raw, err := readRepoFile(repoRoot(t), manualTaskEvidence)
	require.NoError(t, err)
	const prompt = "Write a private-sentinel file, then report its contents."
	for name, payload := range map[string]string{
		"blockquote":      "> " + prompt,
		"indented quote":  "  > " + prompt,
		"escaped quote":   `\u003e ` + prompt,
		"fenced prompt":   "```text\n" + prompt + "\n```",
		"tilde fence":     "~~~\n" + prompt + "\n~~~",
		"indented code":   "    " + prompt,
		"prompt label":    "The prompt was: " + prompt,
		"formatted label": "**Prompt:** " + prompt,
		"prompt heading":  "## Task prompt\n" + prompt,
		"request body":    "Request body: " + prompt,
		"message payload": `{"messages": [{"content": "` + prompt + `"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root, r := fixtureRegister(t)
			for id, it := range r.Items {
				it.Status = "verified"
				it.Evidence = []string{evidencePath + "/source.md"}
				it.AppliedTo = []string{"domain/runtime.go:HostedRoot"}
				if id == "LR-05" || id == "LR-06" {
					it.Status, it.Evidence, it.AppliedTo = "deferred", nil, nil
				}
				if id == "LR-07" || id == "LR-08" {
					it.Evidence = []string{manualTaskEvidence}
				}
				r.Items[id] = it
			}
			path := filepath.Join(root, manualTaskEvidence)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			require.Empty(t, releaseProblems(root, r), "redacted operator results remain release evidence")
			for _, body := range []string{payload + "\n" + string(raw), string(raw) + "\n" + payload} {
				require.NoError(t, os.WriteFile(path, []byte(body), 0600))
				problems := releaseProblems(root, r)
				require.Contains(t, problems, "LR-07: invalid or unsafe observation")
				require.Contains(t, problems, "LR-08: invalid or unsafe observation")
				assert.NotContains(t, strings.Join(problems, " "), "private-sentinel")
			}
		})
	}
}

func TestVerifiedEvidenceReferences(t *testing.T) {
	root, r := fixtureRegister(t)
	it := r.Items["LR-01"]
	it.Status = "verified"
	it.Evidence = []string{evidencePath + "/source.md", evidencePath + "/observations/run.json"}
	it.AppliedTo = []string{"domain/runtime.go:HostedRoot"}
	r.Items["LR-01"] = it
	assert.Empty(t, validateRegisterAt(root, r))
	src, err := os.ReadFile(filepath.Join(root, it.Evidence[0]))
	require.NoError(t, err)
	runtime, err := os.ReadFile(filepath.Join(root, "domain/runtime.go"))
	require.NoError(t, err)
	assert.Contains(t, string(src), provider.LunaRouteHostedRoot)
	assert.Contains(t, string(runtime), provider.LunaRouteHostedRoot)
	require.NoError(t, os.Remove(filepath.Join(root, "domain/runtime.go")))
	assert.NotEmpty(t, validateRegisterAt(root, r), "missing runtime application fails")
}

func TestRegisterRejectsMissingExtraIDs(t *testing.T) {
	root, r := fixtureRegister(t)
	assert.NotEmpty(t, validateRegisterAt(root, register{}))
	delete(r.Items, "LR-08")
	assert.NotEmpty(t, validateRegisterAt(root, r))
	r.Items["LR-09"] = registerItem{Title: "Extra", Status: "decided", Decision: decisionPath + " LR-09"}
	assert.NotEmpty(t, validateRegisterAt(root, r))
}

func TestReleaseGateRejectsPending(t *testing.T) {
	for _, status := range []string{"decided", "escalated", "skip", "unresolved", ""} {
		t.Run(status, func(t *testing.T) {
			root, r := fixtureRegister(t)
			it := r.Items["LR-01"]
			it.Status = status
			it.Escalation = "Obtain published limits before backend enablement"
			r.Items["LR-01"] = it
			assert.NotEmpty(t, releaseProblems(root, r))
		})
	}
	root, r := fixtureRegister(t)
	for id, it := range r.Items {
		it.Status = "verified"
		it.Evidence = []string{evidencePath + "/source.md"}
		it.AppliedTo = []string{"domain/runtime.go:HostedRoot"}
		r.Items[id] = it
	}
	assert.Empty(t, releaseProblems(root, r))
	delete(r.Items, "LR-08")
	assert.NotEmpty(t, releaseProblems(root, r), "release validator itself checks schema")
}

func TestRegisterRejectsBadEvidence(t *testing.T) {
	for _, body := range []string{"", "items: [lr_yaml-secret", "items:\n  LR-01:\n    statuz: lr_yaml-secret\n", "items: {}\n---\nitems: {}"} {
		_, err := parseRegister([]byte(body))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "lr_yaml-secret")
	}
	cases := map[string]func(string, *registerItem){
		"empty evidence": func(_ string, it *registerItem) { it.Evidence = nil },
		"missing":        func(_ string, it *registerItem) { it.Evidence = []string{evidencePath + "/missing.md"} },
		"empty source": func(root string, it *registerItem) {
			require.NoError(t, os.WriteFile(filepath.Join(root, evidencePath, "source.md"), nil, 0600))
		},
		"unreadable": func(root string, it *registerItem) {
			require.NoError(t, os.Chmod(filepath.Join(root, evidencePath, "source.md"), 0000))
		},
		"empty observation": func(root string, it *registerItem) {
			require.NoError(t, os.WriteFile(filepath.Join(root, evidencePath, "observations/run.json"), []byte(`[]`), 0600))
			it.Evidence = []string{evidencePath + "/observations/run.json"}
		},
		"directory": func(_ string, it *registerItem) { it.Evidence = []string{evidencePath + "/observations"} },
		"traversal": func(_ string, it *registerItem) { it.Evidence = []string{evidencePath + "/../../go.mod"} },
		"absolute": func(root string, it *registerItem) {
			it.Evidence = []string{filepath.Join(root, evidencePath, "source.md")}
		},
		"symlink": func(root string, it *registerItem) {
			p := filepath.Join(root, evidencePath, "link.md")
			require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside.md"), p))
			it.Evidence = []string{evidencePath + "/link.md"}
		},
		"secret": func(root string, it *registerItem) {
			require.NoError(t, os.WriteFile(filepath.Join(root, evidencePath, "observations/run.json"), []byte(`{"note":"Bearer foreign-secret"}`), 0600))
			it.Evidence = []string{evidencePath + "/observations/run.json"}
		},
		"malformed observation": func(root string, it *registerItem) {
			require.NoError(t, os.WriteFile(filepath.Join(root, evidencePath, "observations/run.json"), []byte(`{"broken"`), 0600))
			it.Evidence = []string{evidencePath + "/observations/run.json"}
		},
		"no applied":     func(_ string, it *registerItem) { it.AppliedTo = nil },
		"missing symbol": func(_ string, it *registerItem) { it.AppliedTo = []string{"domain/runtime.go:MissingSymbol"} },
		"symbol only in a comment": func(root string, it *registerItem) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "domain/runtime.go"), []byte("// HostedRoot was removed\nconst HostedRootV2 = \"x\"\nfunc use() { _ = HostedRoot }\n"), 0600))
		},
		"bad decision":     func(_ string, it *registerItem) { it.Decision = "https://example.org/decision" },
		"bad status":       func(_ string, it *registerItem) { it.Status = "ready" },
		"empty escalation": func(_ string, it *registerItem) { it.Status = "escalated"; it.Escalation = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root, r := fixtureRegister(t)
			it := r.Items["LR-01"]
			it.Status = "verified"
			it.Evidence = []string{evidencePath + "/source.md"}
			it.AppliedTo = []string{"domain/runtime.go:HostedRoot"}
			change(root, &it)
			r.Items["LR-01"] = it
			problems := validateRegisterAt(root, r)
			require.NotEmpty(t, problems)
			assert.NotContains(t, strings.Join(problems, " "), "foreign-secret")
		})
	}
}

func TestObservationBoundaryRedaction(t *testing.T) {
	key := "lr_test-secret-123"
	cases := []any{
		map[string]any{},
		[]any{},
		map[string]any{"note": `Bearer\nforeign-token`},
		map[string]any{"note": strings.Repeat("x", 256) + key},
		map[string]any{"note": `\u006c\u0072_test-secret-123`},
		map[string]any{"note": `\u0042earer foreign-token`},
		map[string]any{"note": "unrelated lr_ explanation; " + key},
		map[string]any{"note": "lr_other-credential"},
		map[string]any{"note": "x-api-key: foreign-token"},
		map[string]any{"note": "sk-foreign-credential"},
		map[string]any{"prompt": "untrusted request text"},
		map[string]any{"nested": []any{map[string]any{"api_key": "opaque"}}},
		map[string]any{key: "field name also untrusted"},
		json.RawMessage(`{"note":"\u006cr_test-secret-123"}`),
		json.RawMessage(`{"note":"lr_test-secret-123","note":"safe"}`),
		json.RawMessage(`{"prompt":"untrusted raw text","prompt":"string"}`),
	}
	for i, v := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			writes := 0
			err := encodeObservation(key, "safe.json", v, func(string, []byte) error { writes++; return nil })
			require.Error(t, err)
			assert.Zero(t, writes)
			assert.NotContains(t, err.Error(), key)
			assert.NotContains(t, err.Error(), "foreign-token")
		})
	}
	for _, name := range []string{"../outside.json", "/tmp/out.json", "a/b.json", `a\b.json`, ".", "", "lr_bad-token.json"} {
		writes := 0
		require.Error(t, encodeObservation(key, name, map[string]bool{"terminal": true}, func(string, []byte) error { writes++; return nil }))
		assert.Zero(t, writes)
	}
	writes := 0
	require.NoError(t, encodeObservation(key, "good.json", map[string]any{"auth_header": "Authorization", "note": "lr_ is a prefix", "schema": map[string]string{"message": "string"}, "terminal": true}, func(name string, b []byte) error {
		writes++
		assert.Equal(t, "good.json", name)
		assert.True(t, json.Valid(b))
		return nil
	}))
	assert.Equal(t, 1, writes)
	require.Error(t, encodeObservation(key, "good.json", map[string]bool{"terminal": true}, func(string, []byte) error { return fmt.Errorf("write failed %s", key) }))
}

func TestObservationPrivateSink(t *testing.T) {
	root := t.TempDir()
	rel, err := saveObservation(root, "lr_unit-secret", "run.json", map[string]any{"terminal": true, "model": "glm-5.3"})
	require.NoError(t, err)
	assert.Equal(t, evidencePath+"/observations/run.json", rel)
	raw, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"terminal": true`)
	assert.NotContains(t, string(raw), "lr_unit-secret")
	for path, want := range map[string]os.FileMode{filepath.Dir(filepath.Join(root, rel)): 0700, filepath.Join(root, rel): 0600} {
		st, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, want, st.Mode().Perm())
	}
	_, err = saveObservation(root, "lr_unit-secret", "run.json", map[string]bool{"terminal": false})
	require.Error(t, err, "do not overwrite reviewed observations")
	before := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(before, evidencePath), 0700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(before, evidencePath, "observations")))
	_, err = saveObservation(before, "lr_unit-secret", "bad.json", map[string]bool{"terminal": true})
	require.Error(t, err)
}

func TestReleaseGate(t *testing.T) {
	r := loadRegister(t)
	problems := validateRegister(r) // Always validate, even when only this test is selected.
	require.Empty(t, problems, "invalid evidence register")
	pending := releaseProblems(repoRoot(t), r)
	if os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") != "1" {
		if len(pending) > 0 {
			t.Log("release readiness pending: contracts are not all verified")
		}
		return
	}
	require.Empty(t, pending, "release requires verified evidence and runtime application for every LR item")
	// Operator ruling R7 (2026-11-12) descopes the P4-T7 live MCP matrix from this
	// release; readiness rests on the verified register items for chat, codex and pi.
}
