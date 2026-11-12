package lunaroute_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Private comparison material. AI-generated titles are transcript ai-title
// records, NOT SDK session_title_changed events (which report custom names).
type claudeTitleAdoption struct {
	session, title string
}

const (
	claudeTitleMaxCodePoints  = 200 // installed title sanitizer
	claudeProjectKeyMaxLength = 200 // ordinary local project-key producer
)

// Require an unchanged safe title, not an approximation of sanitation rules.
func safeClaudeAdoptedTitle(title, key string) bool {
	if !safeCaptureText(title, key) || strings.TrimSpace(title) != title || utf8.RuneCountInString(title) > claudeTitleMaxCodePoints {
		return false
	}
	for _, r := range title {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func collectClaudeTitleAdoption(raw []byte, session, project string) (claudeTitleAdoption, error) {
	fail := func() (claudeTitleAdoption, error) { return claudeTitleAdoption{}, errAgentEvidence }
	if !safeAgentField(session, "") || project == "" || len(raw) == 0 || len(raw) > captureBodyLimit {
		return fail()
	}
	var proof claudeTitleAdoption
	conversation := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), captureBodyLimit)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !uniqueJSONFields(json.NewDecoder(bytes.NewReader(line))) {
			return fail()
		}
		var entry struct {
			Type      string `json:"type"`
			Session   string `json:"sessionId"`
			Title     string `json:"aiTitle"`
			Cwd       string `json:"cwd"`
			Sidechain bool   `json:"isSidechain"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type == "" {
			return fail()
		}
		switch entry.Type {
		case "user", "assistant":
			if entry.Session != session || entry.Cwd != project || entry.Sidechain {
				return fail()
			}
			conversation = true
		case "ai-title":
			if proof.title != "" || entry.Session != session || entry.Sidechain || !safeClaudeAdoptedTitle(entry.Title, "") {
				return fail()
			}
			proof = claudeTitleAdoption{session: session, title: entry.Title}
		case "custom-title":
			return fail() // A rename cannot establish adoption of the AI response.
		}
	}
	if scanner.Err() != nil || !conversation || proof.title == "" {
		return fail()
	}
	return proof, nil
}

var claudeProjectKeyUnsafe = regexp.MustCompile(`[^a-zA-Z0-9]`)

func readClaudeTitleAdoption(s *claudeIsolation, started, finished time.Time) (claudeTitleAdoption, error) {
	fail := func() (claudeTitleAdoption, error) { return claudeTitleAdoption{}, errAgentEvidence }
	if s == nil || !filepath.IsAbs(s.home) || filepath.Clean(s.home) != s.home || !filepath.IsAbs(s.project) || filepath.Clean(s.project) != s.project || !safeCaptureModel(s.session) || strings.ContainsAny(s.session, "/:") || len(s.project) > claudeProjectKeyMaxLength {
		return fail()
	}
	// The narrow experiment uses the source's ordinary local, short-path
	// project key. Relocated/hashed/storage-backed transcripts are not guessed.
	for _, r := range s.project {
		if r > unicode.MaxASCII {
			return fail()
		}
	}
	dir := filepath.Join(s.home, ".claude", "projects", claudeProjectKeyUnsafe.ReplaceAllString(s.project, "-"))
	// Refuse symlinked/non-directory ancestors before the bounded file read.
	for path := dir; path != s.home; path = filepath.Dir(path) {
		st, err := os.Lstat(path)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fail()
		}
	}
	raw, err := workspaceProof(dir, s.session+".jsonl", captureBodyLimit, started, finished)
	if err != nil {
		return fail()
	}
	return collectClaudeTitleAdoption(raw, s.session, s.project)
}
