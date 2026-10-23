package modelidentity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrMalformedEvidence marks catalog evidence that cannot be verified: a
	// missing identifier, an unparseable release date, or a canonical id that
	// contradicts the native id.
	ErrMalformedEvidence = errors.New("malformed catalog model evidence")
	// ErrUnsupportedNativeID marks a well-formed catalog row whose native id is
	// outside the spelling allow-list (nativeIDPattern). Unlike malformed
	// evidence it does not invalidate its slice: the row alone is skipped, so a
	// remote catalog cannot turn an arbitrary string into a probe's --model value.
	ErrUnsupportedNativeID = errors.New("unsupported catalog native id")
	// ErrUnorderable marks a pair of entries that evidence cannot order: unknown
	// tier or version, a different provider or tier, or equal versions without
	// distinct release dates. An unorderable pair never authorizes advancement.
	ErrUnorderable = errors.New("catalog models are not orderable")
)

// CatalogModel is one parsed catalog row. CanonicalModelID and ReleaseDate are
// optional source fields.
type CatalogModel struct {
	CatalogProvider  string
	NativeID         string
	CanonicalModelID string
	ReleaseDate      string
	SourceRef        string
}

// Entry is the identity of one catalog offering. An empty Tier or OrderKey is the
// explicit "unknown": the entry stays catalogued and visible but is never
// followable.
type Entry struct {
	// CanonicalID unifies one model across the catalog providers that carry it.
	// It never depends on the editable tier patterns.
	CanonicalID string
	// Provider is the catalog provider, which stays the follow target.
	Provider string
	// NativeID is the id exactly as the catalog spells it.
	NativeID string
	// Tier is the policy tier, or empty when no single rule matched.
	Tier string
	// SpecificModel is the executable spelling when NativeID is an explicit,
	// fixed version on its catalog provider; empty otherwise.
	SpecificModel string
	// OrderKey is the normalized dotted numeric version ("5.5"), or empty.
	OrderKey string
	// ReleaseDate is YYYY, YYYY-MM or YYYY-MM-DD, or empty.
	ReleaseDate string
	SourceRef   string
	// Immutable reports that SpecificModel is set.
	Immutable bool
	// Ambiguous reports that more than one rule matched; Tier and OrderKey are
	// then left unknown rather than picking the first hit.
	Ambiguous      bool
	PolicyRevision int64
}

// VersionRequest identifies the model a profile starts a run with. It carries
// only non-secret route metadata.
type VersionRequest struct {
	Model         string
	RouteProvider string
	ConnectionRef string
	Backend       string
}

// VersionResolver turns a profile's model into the fixed executable version to
// pin. It returns ErrSpecificVersionUnavailable for a mutable alias or an opaque
// deployment.
type VersionResolver interface {
	SpecificVersion(context.Context, VersionRequest) (string, error)
}

// family records which catalog providers carry one lab's models, under which
// native-id prefix, and which `lab/` spellings aggregators prepend. It is how a
// model is recognised across providers without consulting the editable policy.
type family struct {
	lab        string
	namePrefix string
	labAliases []string
	providers  []string
}

var families = []family{
	{lab: "anthropic", namePrefix: "claude-", labAliases: []string{"anthropic"}, providers: []string{"anthropic"}},
	{lab: "openai", namePrefix: "gpt-", labAliases: []string{"openai"}, providers: []string{"openai"}},
	{lab: "deepseek", namePrefix: "deepseek-", labAliases: []string{"deepseek"}, providers: []string{"deepseek", "ollama-cloud", "openrouter"}},
	{lab: "zhipuai", namePrefix: "glm-", labAliases: []string{"zhipuai", "z-ai"}, providers: []string{"zhipuai", "ollama-cloud"}},
	{lab: "moonshotai", namePrefix: "kimi-", labAliases: []string{"moonshotai"}, providers: []string{"moonshotai", "ollama-cloud"}},
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// stripRoutingPrefix removes at most one prefix: the catalog provider itself,
// or a recorded lab prefix for a family that provider carries. Policy edits do
// not create routing aliases, and one lab's prefix cannot name another's model.
func stripRoutingPrefix(provider, native string) string {
	prefix, id, ok := strings.Cut(native, "/")
	if !ok || prefix == "" || id == "" {
		return native
	}
	if strings.EqualFold(prefix, provider) {
		return id
	}
	for _, f := range families {
		if contains(f.providers, provider) && contains(f.labAliases, strings.ToLower(prefix)) && strings.HasPrefix(strings.ToLower(id), f.namePrefix) {
			return id
		}
	}
	return native
}

// recognise returns the lab and prefix-stripped, lower-cased id when provider
// carries a family's model, else ok=false.
func recognise(provider, native string) (lab, stripped string, ok bool) {
	s := strings.ToLower(stripRoutingPrefix(provider, native))
	for _, f := range families {
		if contains(f.providers, provider) && strings.HasPrefix(s, f.namePrefix) {
			return f.lab, s, true
		}
	}
	return "", "", false
}

var releaseDateRE = regexp.MustCompile(`^\d{4}(?:-\d{2}(?:-\d{2})?)?$`)

func normalizeReleaseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !releaseDateRE.MatchString(s) {
		return "", fmt.Errorf("%w: release date %q is not YYYY, YYYY-MM or YYYY-MM-DD", ErrMalformedEvidence, s)
	}
	layout := "2006-01-02"[:len(s)]
	if _, err := time.Parse(layout, s); err != nil {
		return "", fmt.Errorf("%w: release date %q is not a calendar date", ErrMalformedEvidence, s)
	}
	return s, nil
}

// nativeIDPattern allow-lists the native ids a catalog row may carry. Every
// discovered id ends up as a harness --model/-m argument on a paid probe, so it
// must start alphanumeric (never a flag) and stay within the characters real
// provider ids use: vendor/model paths, version dots, Bedrock/Ollama ':' tags
// and '@' snapshots, plus one trailing context-window suffix such as Claude
// Code's '[1m]' (claude-sonnet-4-6[1m]). Floating aliases such as OpenRouter's
// '~vendor/x-latest' or Workers AI '@cf/...' paths are not fixed versions and
// fall outside it.
var nativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,200}(\[[0-9]{1,4}[km]\])?$`)

func hasSpace(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0
}

// IdentityMapper maps catalog rows to entries using the persisted policy.
type IdentityMapper struct {
	src PolicySource

	mu       sync.Mutex
	cachedAt int64
	cached   []PatternRule
	matcher  *Matcher
}

// NewIdentityMapper requires a real policy source; nil is a wiring error.
func NewIdentityMapper(src PolicySource) (*IdentityMapper, error) {
	if src == nil {
		return nil, errors.New("modelidentity: nil policy source")
	}
	if v := reflect.ValueOf(src); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, errors.New("modelidentity: nil policy source")
	}
	return &IdentityMapper{src: src}, nil
}

// compiled returns the matcher for the current policy, recompiling only when
// the rules change. An empty policy matches nothing.
func (m *IdentityMapper) compiled(ctx context.Context) (IdentityPolicy, *Matcher, error) {
	p, err := m.src.Snapshot(ctx)
	if err != nil {
		return IdentityPolicy{}, nil, fmt.Errorf("reading identity policy: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cachedAt == p.Revision && reflect.DeepEqual(m.cached, p.Rules) {
		return p, m.matcher, nil
	}
	var matcher *Matcher
	if len(p.Rules) > 0 {
		matcher, err = CompileRules(p.Rules)
		if err != nil {
			return IdentityPolicy{}, nil, fmt.Errorf("compiling identity policy revision %d: %w", p.Revision, err)
		}
	}
	m.cachedAt, m.cached, m.matcher = p.Revision, copyRules(p.Rules), matcher
	return p, matcher, nil
}

// Map classifies one catalog row. Unknown tier or version is returned as an
// Entry with those fields empty, never as a guess; only unverifiable evidence or
// a policy read failure is an error.
func (m *IdentityMapper) Map(ctx context.Context, cm CatalogModel) (Entry, error) {
	provider := strings.ToLower(strings.TrimSpace(cm.CatalogProvider))
	native := cm.NativeID
	date, err := normalizeReleaseDate(cm.ReleaseDate)
	if err != nil {
		return Entry{}, err
	}
	canonical, err := CanonicalID(provider, native, cm.CanonicalModelID)
	if err != nil {
		return Entry{}, err
	}
	policy, matcher, err := m.compiled(ctx)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		CanonicalID:    canonical,
		Provider:       provider,
		NativeID:       native,
		ReleaseDate:    date,
		SourceRef:      cm.SourceRef,
		PolicyRevision: policy.Revision,
	}
	if spec, err := explicitVersion(VersionRequest{Model: native, RouteProvider: provider, ConnectionRef: provider}); err == nil {
		e.SpecificModel, e.Immutable = spec, true
	}
	if matcher == nil {
		return e, nil
	}
	hits := matcher.Match(provider, stripRoutingPrefix(provider, native))
	switch len(hits) {
	case 0:
	case 1:
		if key, ok := orderKey(hits[0].Captures); ok {
			e.Tier, e.OrderKey = strings.ToLower(hits[0].Tier), key
		}
	default:
		e.Ambiguous = true
	}
	return e, nil
}

// CanonicalID is the policy-independent identity: a recognised family model is
// `lab/<id>` on every provider that carries it; a source canonical id is used
// when the family is not recognised; anything else is `provider/native`.
// Retained source inputs can use it to reconcile lifetime observations across
// identity upgrades without reading policy or transferring usability proof.
func CanonicalID(provider, native, source string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch {
	case provider == "" || hasSpace(provider):
		return "", fmt.Errorf("%w: catalog provider %q", ErrMalformedEvidence, provider)
	case native == "" || hasSpace(native):
		return "", fmt.Errorf("%w: native id %q", ErrMalformedEvidence, native)
	case !nativeIDPattern.MatchString(native):
		return "", fmt.Errorf("%w: %q on %s", ErrUnsupportedNativeID, native, provider)
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source != "" && (hasSpace(source) || len(source) > 256) {
		return "", fmt.Errorf("%w: canonical model id %q", ErrMalformedEvidence, source)
	}
	if lab, stripped, ok := recognise(provider, native); ok {
		want := lab + "/" + stripped
		if source != "" && source != stripped && source != want {
			return "", fmt.Errorf("%w: canonical model id %q contradicts native id %q on %s", ErrMalformedEvidence, source, native, provider)
		}
		return want, nil
	}
	if source != "" {
		return source, nil
	}
	return provider + "/" + native, nil
}

// orderKey joins the non-empty captures into a normalized dotted numeric tuple
// ("5","5" -> "5.5"; trailing zero segments are dropped so 5 and 5.0 are equal).
func orderKey(captures []string) (string, bool) {
	var segs []uint32
	for _, c := range captures {
		if c == "" {
			continue
		}
		for _, s := range strings.Split(c, ".") {
			n, err := strconv.ParseUint(s, 10, 32)
			if err != nil {
				return "", false
			}
			segs = append(segs, uint32(n))
		}
	}
	if len(segs) == 0 || len(segs) > 8 {
		return "", false
	}
	for len(segs) > 1 && segs[len(segs)-1] == 0 {
		segs = segs[:len(segs)-1]
	}
	parts := make([]string, len(segs))
	for i, n := range segs {
		parts[i] = strconv.FormatUint(uint64(n), 10)
	}
	return strings.Join(parts, "."), true
}

func parseOrderKey(k string) ([]uint32, bool) {
	if k == "" {
		return nil, false
	}
	var segs []uint32
	for _, s := range strings.Split(k, ".") {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, false
		}
		segs = append(segs, uint32(n))
	}
	return segs, true
}

func sign(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Compare orders two entries of the same provider and tier: 1 when a is newer,
// -1 when older. Numeric versions decide first; only equal versions fall back to
// release dates, and equal versions without distinct dates are ErrUnorderable.
func Compare(a, b Entry) (int, error) {
	fail := func(why string) (int, error) { return 0, fmt.Errorf("%w: %s", ErrUnorderable, why) }
	if a.Tier == "" || b.Tier == "" {
		return fail("tier is unknown")
	}
	if a.Provider != b.Provider || a.Tier != b.Tier {
		return fail("entries differ in provider or tier")
	}
	av, aok := parseOrderKey(a.OrderKey)
	bv, bok := parseOrderKey(b.OrderKey)
	if !aok || !bok {
		return fail("version is unknown")
	}
	for i := 0; i < len(av) || i < len(bv); i++ {
		var x, y uint32
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if c := sign(x, y); c != 0 {
			return c, nil
		}
	}
	if a.ReleaseDate == "" || b.ReleaseDate == "" {
		return fail("equal versions without release dates")
	}
	n := min(len(a.ReleaseDate), len(b.ReleaseDate))
	switch {
	case a.ReleaseDate[:n] < b.ReleaseDate[:n]:
		return -1, nil
	case a.ReleaseDate[:n] > b.ReleaseDate[:n]:
		return 1, nil
	}
	return fail("equal versions with tied release dates")
}
