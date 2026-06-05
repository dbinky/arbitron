package identity

import (
	"reflect"
	"testing"
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
}
