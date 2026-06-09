package identity

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeDept(t *testing.T) {
	cases := map[string]string{
		"Engineering":    "engineering",
		"  engineering ": "engineering",
		"ENGINEERING":    "engineering",
		"":               "",
		"   ":            "",
		"Data Science":   "data science",
	}
	for in, want := range cases {
		if got := NormalizeDept(in); got != want {
			t.Errorf("NormalizeDept(%q) = %q, want %q", in, got, want)
		}
	}
}

// DedupeDepts is the one shared order-preserving dedup primitive (PR #21),
// reused by the dashboard service and the auth middleware. It removes duplicates,
// preserves first-seen order, and is nil-safe. Unlike ResolveDepartments it does
// NOT normalize, drop blanks, or apply the default-group sentinel — those remain
// each caller's own policy.
func TestDedupeDepts(t *testing.T) {
	t.Run("removes duplicates preserving first-seen order", func(t *testing.T) {
		got := DedupeDepts([]string{"engineering", "sales", "engineering", "platform", "sales"})
		want := []string{"engineering", "sales", "platform"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("nil input yields nil", func(t *testing.T) {
		if got := DedupeDepts(nil); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("empty input yields nil", func(t *testing.T) {
		if got := DedupeDepts([]string{}); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("no duplicates returns the same sequence", func(t *testing.T) {
		got := DedupeDepts([]string{"a", "b", "c"})
		if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Errorf("got %v, want [a b c]", got)
		}
	})

	t.Run("does NOT normalize or drop blanks (caller policy)", func(t *testing.T) {
		// distinct-by-exact-value: "Eng" and "eng" are different here.
		got := DedupeDepts([]string{"Eng", "eng", "", ""})
		want := []string{"Eng", "eng", ""}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v (dedupe is value-exact, no normalization)", got, want)
		}
	})
}

func TestResolveDepartments(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}

	t.Run("mixed case and whitespace collapse to one department", func(t *testing.T) {
		got := ResolveDepartments([]string{"Engineering", " engineering ", "ENGINEERING"}, cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("empty claims fall back to default group", func(t *testing.T) {
		got := ResolveDepartments(nil, cfg)
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("all-blank claims fall back to default group", func(t *testing.T) {
		got := ResolveDepartments([]string{"", "   "}, cfg)
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("empty DefaultGroup uses the default sentinel", func(t *testing.T) {
		got := ResolveDepartments(nil, GroupConfig{})
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("duplicates de-duplicated, order preserved", func(t *testing.T) {
		got := ResolveDepartments([]string{"Sales", "eng", "sales", "ENG"}, cfg)
		if !reflect.DeepEqual(got, []string{"sales", "eng"}) {
			t.Errorf("got %v, want [sales eng]", got)
		}
	})
}

func TestDefaultGroupOr(t *testing.T) {
	t.Run("non-empty DefaultGroup is normalized and returned", func(t *testing.T) {
		cfg := GroupConfig{DefaultGroup: "  Engineering  "}
		if got := cfg.DefaultGroupOr(); got != "engineering" {
			t.Errorf("DefaultGroupOr() = %q, want %q", got, "engineering")
		}
	})

	t.Run("empty DefaultGroup returns the sentinel", func(t *testing.T) {
		cfg := GroupConfig{}
		if got := cfg.DefaultGroupOr(); got != "default" {
			t.Errorf("DefaultGroupOr() = %q, want %q", got, "default")
		}
	})

	t.Run("whitespace-only DefaultGroup returns the sentinel", func(t *testing.T) {
		cfg := GroupConfig{DefaultGroup: "   "}
		if got := cfg.DefaultGroupOr(); got != "default" {
			t.Errorf("DefaultGroupOr() = %q, want %q", got, "default")
		}
	})
}

func TestLedDepartments(t *testing.T) {
	cfg := GroupConfig{
		DefaultGroup: "default",
		Leads: map[string][]string{
			"engineering": {"alice@corp.example", "bob-subject-id"},
			"sales":       {"carol@corp.example"},
		},
	}

	t.Run("match by subject", func(t *testing.T) {
		got := LedDepartments("bob-subject-id", "", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("match by email case-insensitively", func(t *testing.T) {
		got := LedDepartments("some-subject", "ALICE@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("no match yields empty", func(t *testing.T) {
		got := LedDepartments("nobody", "nobody@corp.example", cfg)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("nil Leads map yields empty", func(t *testing.T) {
		got := LedDepartments("alice@corp.example", "alice@corp.example", GroupConfig{})
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("blank subject and email yields empty without panicking", func(t *testing.T) {
		got := LedDepartments("", "", cfg)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("multi-dept lead returns sorted result", func(t *testing.T) {
		multiCfg := GroupConfig{
			Leads: map[string][]string{
				"sales":       {"dana@corp.example"},
				"engineering": {"dana@corp.example"},
				"platform":    {"dana@corp.example"},
			},
		}
		got := LedDepartments("", "dana@corp.example", multiCfg)
		// Sorted deterministically regardless of map iteration order.
		want := []string{"engineering", "platform", "sales"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("blank lead identifier in config is skipped", func(t *testing.T) {
		blankCfg := GroupConfig{
			Leads: map[string][]string{
				"engineering": {"", "   ", "alice@corp.example"},
			},
		}
		got := LedDepartments("", "alice@corp.example", blankCfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})
}

func TestEffectiveMembership(t *testing.T) {
	cfg := GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}

	t.Run("lead of D sees D even when token omits it", func(t *testing.T) {
		// alice's token only carries engineering, but she leads platform.
		got := EffectiveMembership([]string{"engineering"}, "alice-subj", "alice@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
			t.Errorf("got %v, want [engineering platform]", got)
		}
	})

	t.Run("non-lead gets only claim departments", func(t *testing.T) {
		got := EffectiveMembership([]string{"engineering"}, "bob-subj", "bob@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("claimless lead gets default group plus led department", func(t *testing.T) {
		got := EffectiveMembership(nil, "alice-subj", "alice@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"default", "platform"}) {
			t.Errorf("got %v, want [default platform]", got)
		}
	})

	t.Run("led department already in claim values is not duplicated", func(t *testing.T) {
		// alice's token already carries "platform" (her led dept); it must appear once.
		got := EffectiveMembership([]string{"engineering", "platform"}, "alice-subj", "alice@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
			t.Errorf("got %v, want [engineering platform] (no dup)", got)
		}
	})

	t.Run("blank subject and email with no claims gets default group only", func(t *testing.T) {
		got := EffectiveMembership(nil, "", "", cfg)
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("local-mode zero config always yields single default sentinel", func(t *testing.T) {
		// In local mode GroupConfig is effectively zero: no GroupClaim (ignored),
		// no Leads, no DefaultGroup. The default sentinel "default" must still be
		// returned so the caller is never in zero departments.
		got := EffectiveMembership(nil, "any-subject", "any@email.com", GroupConfig{})
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default] (local-mode zero config)", got)
		}
	})

	t.Run("claimless multi-dept lead gets default then sorted led depts", func(t *testing.T) {
		// Verifies EffectiveMembership's sort guarantee end-to-end:
		// claim departments first (here: only the default sentinel),
		// then led-only departments in sorted order.
		multiCfg := GroupConfig{
			DefaultGroup: "default",
			Leads: map[string][]string{
				"sales":       {"dana@corp.example"},
				"engineering": {"dana@corp.example"},
				"platform":    {"dana@corp.example"},
			},
		}
		got := EffectiveMembership(nil, "dana-subj", "dana@corp.example", multiCfg)
		want := []string{"default", "engineering", "platform", "sales"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// --- Happy path ---

func TestResolveDepartments_MappedGroup_UsesFriendlyName(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"guid-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

func TestResolveDepartments_UnmappedGroup_UsesVerbatimNormalized(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"Marketing"}, cfg) // no mapping entry
	assert.Equal(t, []string{"marketing"}, got, "verbatim fallback is normalized (lower-cased)")
}

func TestResolveDepartments_MultipleGroups_UnionMappedAndVerbatim(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"guid-123", "sales"}, cfg)
	assert.Equal(t, []string{"engineering", "sales"}, got, "first-seen order preserved")
}

func TestResolveDepartments_NoGroups_FallsBackToDefault(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments(nil, cfg)
	assert.Equal(t, []string{"default"}, got)
}

// --- Input validation ---

func TestResolveDepartments_MappingKeyMatchedCaseInsensitively(t *testing.T) {
	// Keys are normalized at load (Task 2b). ResolveDepartments normalizes the
	// incoming claim before lookup, so a differently-cased claim still hits.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"GUID-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

func TestResolveDepartments_BlankAndWhitespaceClaimsDropped(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}
	got := ResolveDepartments([]string{"  ", "", "eng"}, cfg)
	assert.Equal(t, []string{"eng"}, got)
}

func TestResolveDepartments_AllBlankClaims_FallsBackToDefault(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}
	got := ResolveDepartments([]string{"  ", ""}, cfg)
	assert.Equal(t, []string{"default"}, got)
}

func TestResolveDepartments_DuplicatesAfterMappingCollapsed(t *testing.T) {
	// Two distinct raw claims mapping to the SAME friendly name dedupe to one.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{
		"guid-a": "engineering",
		"guid-b": "engineering",
	}}
	got := ResolveDepartments([]string{"guid-a", "guid-b"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

// TestResolveDepartments_MappedAndVerbatimSameDept_Deduped covers the real-world
// interim migration scenario: a user's token carries both the old GUID claim (which
// maps to "engineering") AND the new verbatim claim "Engineering" (which also
// resolves to "engineering" via normalization). Both paths resolve to the same
// department — the dedup must still produce exactly one occurrence.
func TestResolveDepartments_MappedAndVerbatimSameDept_Deduped(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"guid-123", "Engineering"}, cfg)
	assert.Equal(t, []string{"engineering"}, got,
		"mapped+verbatim claims resolving to the same dept must dedup to one occurrence")
}

func TestResolveDepartments_MappedFriendlyNameNormalized(t *testing.T) {
	// Even if a mapping value somehow reaches here un-normalized, output is normalized.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "Engineering "}}
	got := ResolveDepartments([]string{"guid-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

// TestResolveDepartments_BlankMappingValue_FallsBackToVerbatim covers the subtle
// case where a Mapping entry EXISTS but its value is empty or whitespace. The code
// treats this as "no mapping" and falls through to the verbatim normalized claim.
// A misconfigured mapping entry must not produce an empty department — the user
// ends up in their raw-claim department, not nowhere.
func TestResolveDepartments_BlankMappingValue_FallsBackToVerbatim(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"empty string value", ""},
		{"whitespace-only value", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": tc.value}}
			got := ResolveDepartments([]string{"guid-123"}, cfg)
			assert.Equal(t, []string{"guid-123"}, got,
				"blank mapping value must fall back to verbatim normalized claim, not an empty department")
		})
	}
}
