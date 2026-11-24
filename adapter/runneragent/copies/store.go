// Package copies persists the runner's local workspace copy records.
package copies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
)

// FileStore keeps one JSON record per run under dir (0700), files 0600.
type FileStore struct{ dir string }

var _ runneragent.CopyStateStore = (*FileStore)(nil)

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("copies: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) path(id uuid.UUID) string { return filepath.Join(s.dir, id.String()+".json") }

func (s *FileStore) Get(_ context.Context, id uuid.UUID) (runneragent.CopyState, bool, error) {
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return runneragent.CopyState{}, false, nil
	}
	if err != nil {
		return runneragent.CopyState{}, false, err
	}
	var st runneragent.CopyState
	if err := json.Unmarshal(b, &st); err != nil {
		return runneragent.CopyState{}, false, fmt.Errorf("copies: %s: %w", id, err)
	}
	return st, true, nil
}

func (s *FileStore) Put(_ context.Context, st runneragent.CopyState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(st.RunID))
}

func (s *FileStore) Delete(_ context.Context, id uuid.UUID) error {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileStore) List(ctx context.Context) ([]runneragent.CopyState, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []runneragent.CopyState
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		id, err := uuid.Parse(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		st, ok, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, st)
		}
	}
	return out, nil
}
