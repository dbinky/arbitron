package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// modelsCacheFile is the account model list codex keeps in its home (D4).
const modelsCacheFile = "models_cache.json"

// maxModelsCacheBytes bounds the account model list. An oversized file is
// refused, never partially trusted.
const maxModelsCacheBytes = 4 << 20

// readAccountModels shares the bounded execution-home reader between the probe
// and inventory. Errors never echo account paths or contents.
func readAccountModels(env []string) ([]string, error) {
	home, err := resolveCodexHome(env)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(home, modelsCacheFile))
	if err != nil {
		return nil, fmt.Errorf("%w: account model list unavailable", ErrModelEvidence)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxModelsCacheBytes+1))
	if err != nil || len(data) > maxModelsCacheBytes {
		return nil, fmt.Errorf("%w: account model list unreadable", ErrModelEvidence)
	}
	var list struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&list); err != nil || dec.More() {
		return nil, fmt.Errorf("%w: account model list malformed", ErrModelEvidence)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: account model list malformed", ErrModelEvidence)
	}
	out := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		out = append(out, m.Slug)
	}
	return out, nil
}

// LocalAccountModels returns exact, nonblank, unpadded model slugs in file order.
// An empty or unprovable list is an error, never an unrestricted allowlist.
func LocalAccountModels() ([]string, error) {
	slugs, err := readAccountModels(codexEnv())
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(slugs))
	var out []string
	for _, s := range slugs {
		if s == "" || strings.TrimSpace(s) != s || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: account model list is empty", ErrModelEvidence)
	}
	return out, nil
}

// accountListsModel requires an exact account model slug for usability probes.
func accountListsModel(env []string, model string) error {
	if model == "" {
		return fmt.Errorf("%w: empty model", ErrModelEvidence)
	}
	slugs, err := readAccountModels(env)
	if err != nil {
		return err
	}
	for _, s := range slugs {
		if s == model {
			return nil
		}
	}
	return fmt.Errorf("%w: model not in the account model list", ErrModelEvidence)
}
