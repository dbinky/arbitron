// Package modelidentity holds the admin-editable D2 pattern policy that decides
// which catalog model IDs belong to which (provider, tier) and how their
// versions are ordered.
package modelidentity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
)

const (
	// MaxRules bounds the size of a policy so a bad edit cannot make every
	// refresh evaluate an unbounded number of expressions.
	MaxRules = 256
	// MaxPatternLen bounds one pattern's source length.
	MaxPatternLen = 512
	// MaxProvidersPerRule bounds the provider list of one rule.
	MaxProvidersPerRule = 32
)

var (
	// ErrInvalid marks a policy edit that failed validation.
	ErrInvalid = errors.New("invalid identity policy")
	// ErrConflict marks an update made against a stale policy revision.
	ErrConflict = errors.New("identity policy revision conflict")
	// ErrNoMutationGate marks an update attempted before a mutation gate was injected.
	ErrNoMutationGate = errors.New("identity policy mutation gate not configured")
)

// PatternRule maps catalog model IDs matching Pattern (anchored, full match) on
// any of Providers to Tier. VersionGroups are the 1-based capture groups that
// form the numeric version tuple, in order.
type PatternRule struct {
	Providers     []string `json:"providers"`
	Tier          string   `json:"tier"`
	Pattern       string   `json:"pattern"`
	VersionGroups []int    `json:"version_groups"`
}

// IdentityPolicy is one revision of the full rule table. Revision starts at 1 and
// is advanced only by the store.
type IdentityPolicy struct {
	Revision int64         `json:"revision"`
	Rules    []PatternRule `json:"rules"`
}

// PolicySource supplies the current policy to the mapper and refresh paths.
type PolicySource interface {
	Snapshot(context.Context) (IdentityPolicy, error)
}

// PolicyStore persists the policy. Replace swaps the whole rule set only when
// expected is the current revision, and returns ErrConflict otherwise.
type PolicyStore interface {
	PolicySource
	Replace(ctx context.Context, expected int64, rules []PatternRule) (IdentityPolicy, error)
}

// MutationGate serializes a policy write with refresh and advancement. The
// execution gate satisfies it; the narrow interface avoids a construction cycle.
type MutationGate interface {
	WithMutation(ctx context.Context, fn func(context.Context) error) error
}

func copyRule(r PatternRule) PatternRule {
	return PatternRule{
		Providers:     append([]string(nil), r.Providers...),
		Tier:          r.Tier,
		Pattern:       r.Pattern,
		VersionGroups: append([]int(nil), r.VersionGroups...),
	}
}

func copyRules(rules []PatternRule) []PatternRule {
	out := make([]PatternRule, len(rules))
	for i, r := range rules {
		out[i] = copyRule(r)
	}
	return out
}

func (p IdentityPolicy) clone() IdentityPolicy {
	return IdentityPolicy{Revision: p.Revision, Rules: copyRules(p.Rules)}
}

// NormalizeRules lower-cases and trims providers and tiers, drops duplicate
// providers within a rule, and preserves model-pattern spelling. It returns a copy.
func NormalizeRules(rules []PatternRule) []PatternRule {
	out := copyRules(rules)
	for i := range out {
		out[i].Tier = strings.ToLower(strings.TrimSpace(out[i].Tier))
		seen := map[string]bool{}
		providers := out[i].Providers[:0]
		for _, p := range out[i].Providers {
			p = strings.ToLower(strings.TrimSpace(p))
			if seen[p] {
				continue
			}
			seen[p] = true
			providers = append(providers, p)
		}
		out[i].Providers = providers
	}
	return out
}

// anchored wraps a pattern so it can only match the whole model ID.
func anchored(pattern string) string { return `^(?:` + pattern + `)$` }

// ValidateRules checks a full rule set. It does not attempt to prove that two
// different patterns never match the same ID; the mapper rejects such catalog
// IDs as ambiguous when it sees them.
func ValidateRules(rules []PatternRule) error {
	_, err := compile(rules)
	return err
}

type compiledRule struct {
	rule PatternRule
	re   *regexp.Regexp
}

func compile(rules []PatternRule) ([]compiledRule, error) {
	if len(rules) == 0 {
		return nil, fmt.Errorf("%w: at least one rule is required", ErrInvalid)
	}
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("%w: at most %d rules are allowed", ErrInvalid, MaxRules)
	}
	// Bound the submitted lists before normalization can collapse duplicates.
	for i, r := range rules {
		if len(r.Providers) == 0 || len(r.Providers) > MaxProvidersPerRule {
			return nil, fmt.Errorf("%w: rule %d: between 1 and %d providers are required", ErrInvalid, i+1, MaxProvidersPerRule)
		}
	}
	rules = NormalizeRules(rules)
	out := make([]compiledRule, 0, len(rules))
	// (provider, pattern) -> tier, to refuse two rules that claim the same IDs for different targets.
	targets := map[[2]string]string{}
	for i, r := range rules {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("%w: rule %d: %s", ErrInvalid, i+1, fmt.Sprintf(format, args...))
		}
		for _, p := range r.Providers {
			if p == "" {
				return nil, fail("provider must not be empty")
			}
		}
		if r.Tier == "" {
			return nil, fail("tier must not be empty")
		}
		if r.Pattern == "" || len(r.Pattern) > MaxPatternLen {
			return nil, fail("pattern must be 1 to %d characters", MaxPatternLen)
		}
		tree, err := syntax.Parse(anchored(r.Pattern), syntax.Perl)
		if err != nil {
			return nil, fail("pattern does not compile: %v", err)
		}
		re, err := regexp.Compile(anchored(r.Pattern))
		if err != nil {
			return nil, fail("pattern does not compile: %v", err)
		}
		if len(r.VersionGroups) == 0 {
			return nil, fail("at least one version group is required")
		}
		seen := map[int]bool{}
		for _, g := range r.VersionGroups {
			if g < 1 || g > re.NumSubexp() {
				return nil, fail("version group %d is not a capture group of the pattern", g)
			}
			if seen[g] {
				return nil, fail("version group %d is repeated", g)
			}
			seen[g] = true
			if !numericCapture(tree, g) {
				return nil, fail("version group %d must match a nonempty dotted numeric tuple", g)
			}
		}
		for _, p := range r.Providers {
			k := [2]string{p, r.Pattern}
			if tier, ok := targets[k]; ok && tier != r.Tier {
				return nil, fail("pattern already assigns %s/%s to tier %q", p, r.Pattern, tier)
			}
			targets[k] = r.Tier
		}
		out = append(out, compiledRule{rule: r, re: re})
	}
	return out, nil
}

// numericCapture reports whether every value of capture group idx is a
// nonempty dotted numeric tuple. An optional capture may be absent, but when
// participating it cannot contain empty components or leading/trailing dots.
func numericCapture(tree *syntax.Regexp, idx int) bool {
	var find func(*syntax.Regexp) *syntax.Regexp
	find = func(n *syntax.Regexp) *syntax.Regexp {
		if n.Op == syntax.OpCapture && n.Cap == idx {
			return n
		}
		for _, s := range n.Sub {
			if f := find(s); f != nil {
				return f
			}
		}
		return nil
	}
	group := find(tree)
	if group == nil {
		return false
	}
	var digitsOnly func(*syntax.Regexp) bool
	digitsOnly = func(n *syntax.Regexp) bool {
		switch n.Op {
		case syntax.OpLiteral:
			for _, r := range n.Rune {
				if (r < '0' || r > '9') && r != '.' {
					return false
				}
			}
			return true
		case syntax.OpCharClass:
			for i := 0; i+1 < len(n.Rune); i += 2 {
				lo, hi := n.Rune[i], n.Rune[i+1]
				if !(lo >= '0' && hi <= '9') && !(lo == '.' && hi == '.') {
					return false
				}
			}
			return true
		case syntax.OpEmptyMatch:
			return true
		case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat, syntax.OpConcat, syntax.OpAlternate:
			for _, s := range n.Sub {
				if !digitsOnly(s) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	for _, s := range group.Sub {
		if !digitsOnly(s) {
			return false
		}
	}
	return numericTupleGrammar(group.Sub[0])
}

// numericTupleGrammar walks the regexp program paired with the two states of
// the numeric grammar: expecting a digit, or having read digits. The visited
// set bounds traversal even for unbounded repetition; sampling matches cannot
// establish that a policy's entire capture language is numeric.
func numericTupleGrammar(tree *syntax.Regexp) bool {
	program, err := syntax.Compile(tree.Simplify())
	if err != nil {
		return false
	}
	type state struct {
		pc       uint32
		inDigits bool
	}
	pending := []state{{pc: uint32(program.Start)}}
	seen := map[state]bool{}
	for len(pending) > 0 {
		s := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		inst := program.Inst[s.pc]
		switch inst.Op {
		case syntax.InstMatch:
			if !s.inDigits {
				return false
			}
		case syntax.InstAlt, syntax.InstAltMatch:
			pending = append(pending, state{inst.Out, s.inDigits}, state{inst.Arg, s.inDigits})
		case syntax.InstCapture, syntax.InstNop:
			pending = append(pending, state{inst.Out, s.inDigits})
		case syntax.InstRune, syntax.InstRune1:
			// numericCapture already restricts the alphabet to ASCII digits/dot.
			if inst.MatchRune('.') {
				if !s.inDigits {
					return false
				}
				pending = append(pending, state{pc: inst.Out})
			}
			for digit := '0'; digit <= '9'; digit++ {
				if inst.MatchRune(digit) {
					pending = append(pending, state{inst.Out, true})
					break
				}
			}
		case syntax.InstFail:
			// This branch accepts no strings.
		default:
			return false
		}
	}
	return true
}

// Hit is one rule that matched a catalog ID.
type Hit struct {
	Tier string
	// Captures are the raw text of the rule's VersionGroups, in order.
	Captures []string
}

// Matcher evaluates a compiled policy.
type Matcher struct{ rules []compiledRule }

// CompileRules validates and compiles a rule set for matching.
func CompileRules(rules []PatternRule) (*Matcher, error) {
	c, err := compile(rules)
	if err != nil {
		return nil, err
	}
	return &Matcher{rules: c}, nil
}

// Match returns every rule that fully matches modelID on provider. More than one
// hit means the ID is ambiguous; callers must not pick the first.
func (m *Matcher) Match(provider, modelID string) []Hit {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var hits []Hit
	for _, c := range m.rules {
		onProvider := false
		for _, p := range c.rule.Providers {
			if p == provider {
				onProvider = true
				break
			}
		}
		if !onProvider {
			continue
		}
		sub := c.re.FindStringSubmatch(modelID)
		if sub == nil {
			continue
		}
		caps := make([]string, 0, len(c.rule.VersionGroups))
		for _, g := range c.rule.VersionGroups {
			caps = append(caps, sub[g])
		}
		hits = append(hits, Hit{Tier: c.rule.Tier, Captures: caps})
	}
	return hits
}
