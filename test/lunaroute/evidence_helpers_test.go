package lunaroute_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	evidencePath     = "docs/evidence/lunaroute-provider"
	decisionPath     = "docs/designs/2026-11-10-lunaroute-provider-decisions.md"
	maxEvidenceBytes = 4 << 20
)

type registerItem struct {
	Title      string   `yaml:"title"`
	Status     string   `yaml:"status"`
	Decision   string   `yaml:"decision"`
	Evidence   []string `yaml:"evidence,omitempty"`
	Escalation string   `yaml:"escalation"`
	AppliedTo  []string `yaml:"applied_to,omitempty"`
}
type register struct {
	Items map[string]registerItem `yaml:"items"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate evidence helper source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
func evidenceDir(t *testing.T) string { t.Helper(); return filepath.Join(repoRoot(t), evidencePath) }

func parseRegister(raw []byte) (register, error) {
	var r register
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if err := d.Decode(&r); err != nil {
		return register{}, errors.New("invalid register YAML")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return register{}, errors.New("register must contain one YAML document")
	}
	if len(r.Items) == 0 {
		return register{}, errors.New("empty register")
	}
	return r, nil
}
func loadRegister(t *testing.T) register {
	t.Helper()
	raw, err := readRepoFile(repoRoot(t), evidencePath+"/register.yaml")
	if err != nil {
		t.Fatal("cannot read evidence register")
	}
	r, err := parseRegister(raw)
	if err != nil {
		t.Fatal(err.Error())
	}
	return r
}
func validateRegister(r register) []string { return validateRegisterAt(sourceRoot(), r) }
func sourceRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

// Errors contain only fixed labels/known LR ids, never untrusted YAML or files.
func validateRegisterAt(root string, r register) []string {
	var problems []string
	if len(r.Items) != 8 {
		problems = append(problems, "register must have exactly LR-01 through LR-08")
	}
	for id := range r.Items {
		if !regexp.MustCompile(`^LR-0[1-8]$`).MatchString(id) {
			problems = append(problems, "unexpected register id")
		}
	}
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("LR-%02d", i)
		it, ok := r.Items[id]
		if !ok {
			problems = append(problems, id+": missing item")
			continue
		}
		bad := func(msg string) { problems = append(problems, id+": "+msg) }
		if strings.TrimSpace(it.Title) == "" {
			bad("missing title")
		}
		if it.Decision != decisionPath+" "+id {
			bad("missing canonical decision reference")
		} else if raw, err := readRepoFile(root, decisionPath); err != nil || !bytes.Contains(raw, []byte(id)) {
			bad("decision source unavailable")
		}
		switch it.Status {
		case "decided", "deferred":
		case "verified":
			if len(it.Evidence) == 0 {
				bad("verified requires evidence")
			}
			if len(it.AppliedTo) == 0 {
				bad("verified requires runtime application")
			}
		case "escalated":
			if strings.TrimSpace(it.Escalation) == "" {
				bad("escalation requires an actionable explanation")
			}
		default:
			bad("invalid status")
		}
		for _, ref := range it.Evidence {
			if !strings.HasPrefix(ref, evidencePath+"/") {
				bad("evidence must be repo-relative inside the evidence directory")
				continue
			}
			raw, err := readRepoFile(root, ref)
			if err != nil {
				bad("evidence unavailable")
				continue
			}
			if strings.HasPrefix(ref, evidencePath+"/observations/") {
				if ref == evidencePath+"/observations/operator-manual-agent-tasks-2026-11-12.md" {
					// R7 supplies this reviewed operator record instead of a generated collector artifact.
					if unsafeText(string(raw), "") {
						bad("invalid or unsafe observation")
					}
				} else if filepath.Ext(ref) != ".json" || validateObservationJSON(raw, "") != nil {
					bad("invalid or unsafe observation")
				}
			}
		}
		for _, ref := range it.AppliedTo {
			path, symbol, ok := strings.Cut(ref, ":")
			if !ok || strings.TrimSpace(symbol) == "" {
				bad("application requires file:symbol")
				continue
			}
			raw, err := readRepoFile(root, path)
			if err != nil || !declaresSymbol(raw, symbol) {
				bad("runtime application unavailable")
			}
		}
	}
	return problems
}

// declaresSymbol reports whether raw declares symbol: a Go func (or method),
// type, var or const, or a top-level YAML key. A substring or a comment
// mention is not a live symbol, so a renamed target cannot leave a stale pointer.
func declaresSymbol(raw []byte, symbol string) bool {
	name := regexp.QuoteMeta(symbol)
	return regexp.MustCompile(`(?m)^(?:(?:func\s+(?:\([^)]*\)\s*)?|type\s+|var\s+|const\s+)` + name + `\b|` + name + `:)`).Match(raw)
}

func releaseProblems(root string, r register) []string {
	problems := validateRegisterAt(root, r)
	// R7 defers LR-05/LR-06, not evidence or application for released paths.
	for _, id := range []string{"LR-01", "LR-02", "LR-03", "LR-04", "LR-07", "LR-08"} {
		if r.Items[id].Status != "verified" {
			problems = append(problems, id+": verification pending")
		}
	}
	return problems
}

// Reject traversal and symlinks at every component, including in-repo symlinks.
func safeRepoPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") || filepath.ToSlash(filepath.Clean(rel)) != rel || rel == "." || strings.HasPrefix(rel, "../") {
		return "", errors.New("unsafe repository path")
	}
	p := root
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		st, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return "", errors.New("unreadable repository path")
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink repository path")
		}
	}
	return p, nil
}
func readRepoFile(root, rel string) ([]byte, error) {
	p, err := safeRepoPath(root, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, errors.New("repository file unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0444 == 0 {
		return nil, errors.New("repository file is not readable regular data")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxEvidenceBytes+1))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 || len(raw) > maxEvidenceBytes {
		return nil, errors.New("empty, unreadable or oversized evidence")
	}
	return raw, nil
}

var (
	secretToken   = regexp.MustCompile(`(?i)(lr_[a-z0-9][a-z0-9._~+/=-]*|sk-[a-z0-9_-]+|bearer\s+\S+|(?:x-api-key|lunaroute-api-key|authorization)\s*[:=]\s*\S+)`)
	unicodeEscape = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)
)

func decodedText(s string) string {
	for {
		next := unicodeEscape.ReplaceAllStringFunc(s, func(match string) string { n, _ := strconv.ParseUint(match[2:], 16, 16); return string(rune(n)) })
		next = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\/`, "/", `\\`, `\`).Replace(next)
		if next == s {
			return s
		}
		s = next
	}
}
func unsafeText(s, key string) bool {
	s = decodedText(s)
	return key != "" && strings.Contains(s, key) || secretToken.MatchString(s)
}

// JSON object members must be unique before converting to maps. Otherwise a
// later schema marker could conceal an earlier raw prompt/body in the file.
func uniqueJSONFields(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !uniqueJSONFields(d) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !uniqueJSONFields(d) {
				return false
			}
		}
	default:
		return false
	}
	_, err = d.Token()
	return err == nil
}

func validateObservationJSON(raw []byte, key string) error {
	// Scan the full encoded input as well as decoded values: duplicate JSON
	// members must not hide an overwritten credential. Never truncate input.
	if unsafeText(string(raw), key) {
		return errors.New("unsafe observation content")
	}
	var v any
	if len(raw) > maxEvidenceBytes || json.Unmarshal(raw, &v) != nil || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
		return errors.New("invalid observation JSON")
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return errors.New("empty observation")
		}
	case []any:
		if len(x) == 0 {
			return errors.New("empty observation")
		}
	default:
		return errors.New("observation must be a nonempty object or array")
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case string:
			return !unsafeText(x, key)
		case []any:
			for _, v := range x {
				if !walk(v) {
					return false
				}
			}
		case map[string]any:
			for k, v := range x {
				if unsafeText(k, key) {
					return false
				}
				normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(decodedText(k), "_", ""), "-", ""))
				switch normalized {
				case "apikey", "authorization", "password", "secret", "accesstoken", "refreshtoken", "headers", "env":
					return false
				case "prompt", "body", "message", "messages", "content", "stderr", "stdout":
					// A schema may name these fields, but never retain their values.
					s, ok := v.(string)
					if !ok || (s != "string" && s != "array" && s != "object" && s != "null") {
						return false
					}
				}
				if !walk(v) {
					return false
				}
			}
		}
		return true
	}
	if v == nil || !walk(v) {
		return errors.New("unsafe observation content")
	}
	return nil
}
func encodeObservation(key, name string, v any, write func(string, []byte) error) error {
	if name == "" || name == "." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || filepath.Ext(name) != ".json" || unsafeText(name, key) {
		return errors.New("observation requires a safe JSON basename")
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if enc.Encode(v) != nil {
		return errors.New("cannot encode observation")
	}
	if err := validateObservationJSON(b.Bytes(), key); err != nil {
		return err
	}
	if write(name, b.Bytes()) != nil {
		return errors.New("cannot write observation")
	}
	return nil
}
func saveObservation(root, key, name string, v any) (string, error) {
	var rel string
	err := encodeObservation(key, name, v, func(name string, b []byte) error {
		rel = evidencePath + "/observations/" + name
		p, err := safeRepoPath(root, rel)
		if err != nil {
			return err
		}
		dir := filepath.Dir(p)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err = os.Chmod(dir, 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, werr := f.Write(b)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(p)
			return errors.New("observation write failed")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return rel, nil
}
func writeObservation(t *testing.T, key, name string, v any) string {
	t.Helper()
	path, err := saveObservation(repoRoot(t), key, name, v)
	if err != nil {
		t.Fatal(err.Error())
	}
	return path
}
