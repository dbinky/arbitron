package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// modelsCacheFile is the account model list codex keeps in its home (D4).
const modelsCacheFile = "models_cache.json"

// maxModelsCacheBytes bounds the account model list. An oversized file is
// refused, never partially trusted.
const maxModelsCacheBytes = 4 << 20

// accountListsModel admits a usability probe of model only when the account's
// models_cache.json (in the same codex home the launch env selects) lists it as
// an exact `.models[].slug` (D4). A missing, unreadable, oversized or malformed
// list refuses: an unprovable listing is never a listing. Errors never echo the
// file path or contents.
func accountListsModel(env []string, model string) error {
	if model == "" {
		return fmt.Errorf("%w: empty model", ErrModelEvidence)
	}
	home, err := resolveCodexHome(env)
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(home, modelsCacheFile))
	if err != nil {
		return fmt.Errorf("%w: account model list unavailable", ErrModelEvidence)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxModelsCacheBytes+1))
	if err != nil || len(data) > maxModelsCacheBytes {
		return fmt.Errorf("%w: account model list unreadable", ErrModelEvidence)
	}
	var list struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&list); err != nil || dec.More() {
		return fmt.Errorf("%w: account model list malformed", ErrModelEvidence)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: account model list malformed", ErrModelEvidence)
	}
	for _, m := range list.Models {
		if m.Slug == model {
			return nil
		}
	}
	return fmt.Errorf("%w: model not in the account model list", ErrModelEvidence)
}
