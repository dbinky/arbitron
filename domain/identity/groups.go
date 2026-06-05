package identity

import "strings"

// GroupConfig is the resolution-time RBAC configuration the group-resolution
// helpers consume. It is defined HERE (the dependency floor) rather than in
// domain/config so the helpers stay pure and no config<->identity import cycle
// forms; adapter wiring maps config.RBACConfig (Task 2) into this shape, exactly
// as adapter/oidc maps config.OIDCConfig into oidc.Config.
type GroupConfig struct {
	// GroupClaim is the token claim name carrying group membership. "" means no
	// claim is read and every user falls back to the default group.
	GroupClaim string
	// DefaultGroup is the sentinel department for claimless users. Callers MUST
	// pass a non-empty, already-normalized value (the loader defaults it to
	// "default"); DefaultGroupOr applies the "default" fallback defensively.
	DefaultGroup string
	// Leads maps a normalized department name to the lead identifiers (subjects
	// and/or emails, lower-cased) that lead it.
	Leads map[string][]string
}

// defaultGroupSentinel is the hard fallback when GroupConfig.DefaultGroup is
// empty, so resolution never yields an empty department set.
const defaultGroupSentinel = "default"

// NormalizeDept canonicalizes a raw claim/department value: trimmed and
// lower-cased. An all-whitespace input normalizes to "".
func NormalizeDept(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// DefaultGroupOr returns cfg.DefaultGroup normalized, or the "default" sentinel
// when it is empty/blank.
func (cfg GroupConfig) DefaultGroupOr() string {
	if d := NormalizeDept(cfg.DefaultGroup); d != "" {
		return d
	}
	return defaultGroupSentinel
}

// DedupeDepts is the one shared order-preserving department dedup primitive: it
// removes duplicate values while preserving first-seen order, and is nil-safe (a
// nil/empty input yields nil). It is the canonical RBAC dedup reused by the
// dashboard service and the auth middleware so the operation lives in exactly one
// place (PR #21). It deliberately does NOT normalize, drop blanks, or apply the
// default-group sentinel — those are each caller's own policy (see
// ResolveDepartments / EffectiveMembership, which layer their policy on top).
func DedupeDepts(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

// ResolveDepartments normalizes the raw claim values, drops blanks, and
// de-duplicates while preserving first-seen order. If the result is empty (no
// claim configured, claim absent, or all values blank), it returns the single
// default-group sentinel so a caller is never in zero departments.
func ResolveDepartments(claimValues []string, cfg GroupConfig) []string {
	// Normalize + drop blanks (this function's own policy), then dedupe via the
	// shared primitive (the operation both this and EffectiveMembership share).
	normalized := make([]string, 0, len(claimValues))
	for _, v := range claimValues {
		if d := NormalizeDept(v); d != "" {
			normalized = append(normalized, d)
		}
	}
	out := DedupeDepts(normalized)
	if len(out) == 0 {
		return []string{cfg.DefaultGroupOr()}
	}
	return out
}

// LedDepartments returns the normalized departments this identity is a
// configured lead of, matched case-insensitively against the subject OR the
// email (mirroring the bootstrap-admin match rule in
// instance.Provisioner.IsBootstrapAdmin). Result order follows sorted department
// keys for determinism. An empty/blank subject and email yields no matches.
func LedDepartments(subject, email string, cfg GroupConfig) []string {
	if len(cfg.Leads) == 0 {
		return nil
	}
	subj := strings.ToLower(strings.TrimSpace(subject))
	em := strings.ToLower(strings.TrimSpace(email))
	if subj == "" && em == "" {
		return nil
	}
	// Collect matching departments, then sort for deterministic output.
	matched := make([]string, 0, len(cfg.Leads))
	for dept, ids := range cfg.Leads {
		for _, id := range ids {
			lid := strings.ToLower(strings.TrimSpace(id))
			if lid == "" {
				continue
			}
			if (subj != "" && lid == subj) || (em != "" && lid == em) {
				matched = append(matched, NormalizeDept(dept))
				break
			}
		}
	}
	sortStrings(matched)
	return matched
}

// sortStrings is an in-package insertion sort kept here so groups.go stays
// import-light (no sort import alongside strings); the lists are tiny (number of
// led departments), so the simple sort is fine.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// EffectiveMembership is the caller's full department set: the claim-derived
// departments (ResolveDepartments) unioned with the departments they lead
// (LedDepartments). A configured lead of a department therefore always sees that
// department even if their token omits it. Order: claim/default departments
// first (first-seen), then any newly-added led departments (sorted).
func EffectiveMembership(claimValues []string, subject, email string, cfg GroupConfig) []string {
	// Union = claim/default departments first, then led departments, de-duplicated
	// order-preserving via the shared primitive. ResolveDepartments and
	// LedDepartments each own their own normalization policy; the union itself is
	// just an order-preserving dedup.
	claim := ResolveDepartments(claimValues, cfg)
	return DedupeDepts(append(claim, LedDepartments(subject, email, cfg)...))
}
