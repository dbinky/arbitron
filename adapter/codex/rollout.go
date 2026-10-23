package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxRolloutBytes bounds the whole evidence file, including tool output and
// line delimiters. Oversized rollouts are refused, never partially trusted.
const maxRolloutBytes = 1 << 20

// ErrModelEvidence marks a pinned Codex turn whose rollout evidence is missing,
// from another session, from an earlier turn, or names another model. The turn
// is refused rather than trusted from argv. Evidence-derived identities may
// contain secrets; surfaced diagnostics must not echo those fields.
var ErrModelEvidence = errors.New("codex: executed-model evidence rejected")

// threadIDPattern admits only codex-issued thread ids (UUID-shaped), so an id
// can never widen the rollout glob or escape the sessions directory.
var threadIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{7,63}$`)

// resolveCodexHome returns the account home codex itself uses for this launch
// env: CODEX_HOME when set, else <HOME>/.codex. Reading the same env slice the
// subprocess receives keeps the reader and the harness on one home.
func resolveCodexHome(env []string) (string, error) {
	var home, codexHome string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, codexHomeVar+"="):
			codexHome = strings.TrimSpace(strings.TrimPrefix(kv, codexHomeVar+"="))
		case strings.HasPrefix(kv, "HOME="):
			home = strings.TrimSpace(strings.TrimPrefix(kv, "HOME="))
		}
	}
	if codexHome != "" {
		return codexHome, nil
	}
	if home == "" {
		return "", fmt.Errorf("%w: cannot resolve codex home", ErrModelEvidence)
	}
	return filepath.Join(home, ".codex"), nil
}

// rolloutEvidence is what a session's stored rollout proves: its own thread id
// (session_meta) and the model of every turn_context in file order. Compaction
// can record more than one context for a turn.
type rolloutEvidence struct {
	ThreadID   string
	TurnModels []string
}

// findRollout locates exactly one stored rollout for threadID under
// <home>/sessions/YYYY/MM/DD. It never falls back to "the latest rollout".
func findRollout(home, threadID string) (string, error) {
	if !threadIDPattern.MatchString(threadID) {
		return "", fmt.Errorf("%w: malformed thread id", ErrModelEvidence)
	}
	matches, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrModelEvidence, err)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: no rollout for requested thread", ErrModelEvidence)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%w: %d rollouts for requested thread", ErrModelEvidence, len(matches))
	}
}

// readRollout collects the session_meta id and the model of each turn_context.
// Read at most the limit plus one byte before decoding anything, so a growing
// file cannot bypass the cap and a refused file never supplies partial evidence.
func readRollout(path string) (rolloutEvidence, error) {
	f, err := os.Open(path)
	if err != nil {
		return rolloutEvidence{}, fmt.Errorf("%w: cannot open rollout", ErrModelEvidence)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxRolloutBytes+1))
	if err != nil {
		return rolloutEvidence{}, fmt.Errorf("%w: cannot read rollout", ErrModelEvidence)
	}
	if len(data) > maxRolloutBytes {
		return rolloutEvidence{}, fmt.Errorf("%w: rollout exceeds %d bytes", ErrModelEvidence, maxRolloutBytes)
	}

	var ev rolloutEvidence
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Type == "" {
			return rolloutEvidence{}, fmt.Errorf("%w: malformed rollout record", ErrModelEvidence)
		}
		if rec.Type != "session_meta" && rec.Type != "turn_context" {
			continue
		}
		var payload struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			return rolloutEvidence{}, fmt.Errorf("%w: malformed rollout identity", ErrModelEvidence)
		}
		switch rec.Type {
		case "session_meta":
			if payload.ID == "" || (ev.ThreadID != "" && ev.ThreadID != payload.ID) {
				return rolloutEvidence{}, fmt.Errorf("%w: missing or contradictory rollout session", ErrModelEvidence)
			}
			ev.ThreadID = payload.ID
		case "turn_context":
			ev.TurnModels = append(ev.TurnModels, payload.Model)
		}
	}
	return ev, nil
}

// rolloutVerifier proves a pinned Codex turn ran on the pinned model from the
// session's own stored rollout (never from argv or the stream).
type rolloutVerifier struct {
	home string
}

func newRolloutVerifier(env []string) (*rolloutVerifier, error) {
	home, err := resolveCodexHome(env)
	if err != nil {
		return nil, err
	}
	return &rolloutVerifier{home: home}, nil
}

func (v *rolloutVerifier) evidence(threadID string) (rolloutEvidence, error) {
	path, err := findRollout(v.home, threadID)
	if err != nil {
		return rolloutEvidence{}, err
	}
	ev, err := readRollout(path)
	if err != nil {
		return rolloutEvidence{}, err
	}
	if ev.ThreadID != threadID {
		return rolloutEvidence{}, fmt.Errorf("%w: rollout belongs to another thread", ErrModelEvidence)
	}
	return ev, nil
}

// beforeResume checks the stored session before it is resumed: it must have at
// least one recorded turn and every one must have run on pin. It returns the
// recorded turn count so afterTurn can require exactly the new turn's evidence.
func (v *rolloutVerifier) beforeResume(threadID, pin string) (int, error) {
	ev, err := v.evidence(threadID)
	if err != nil {
		return 0, err
	}
	if len(ev.TurnModels) == 0 {
		return 0, fmt.Errorf("%w: no turn_context to prove model %q", ErrModelEvidence, pin)
	}
	for i, m := range ev.TurnModels {
		if m != pin {
			return 0, fmt.Errorf("%w: turn %d did not prove pinned model %q", ErrModelEvidence, i+1, pin)
		}
	}
	return len(ev.TurnModels), nil
}

// afterTurn checks the completed turn: the rollout must have recorded at least
// one turn_context beyond priorTurns, and every new one must be pin. Earlier
// turns can therefore never satisfy the proof for this one.
func (v *rolloutVerifier) afterTurn(threadID, pin string, priorTurns int) error {
	ev, err := v.evidence(threadID)
	if err != nil {
		return err
	}
	if len(ev.TurnModels) <= priorTurns {
		return fmt.Errorf("%w: no new turn_context (had %d, now %d)", ErrModelEvidence, priorTurns, len(ev.TurnModels))
	}
	for i := priorTurns; i < len(ev.TurnModels); i++ {
		if ev.TurnModels[i] != pin {
			return fmt.Errorf("%w: turn %d did not prove pinned model %q", ErrModelEvidence, i+1, pin)
		}
	}
	return nil
}
