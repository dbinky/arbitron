package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// durableFiles keeps syscall failures injectable per repository/journal, without
// process-global test hooks or a production durability opt-out.
type durableFiles struct {
	sync   func(*os.File) error
	rename func(string, string) error
}

func (d durableFiles) renamePath(from, to string) error {
	if d.rename != nil {
		return d.rename(from, to)
	}
	return os.Rename(from, to)
}

func (d durableFiles) syncFile(f *os.File) error {
	if d.sync != nil {
		return d.sync(f)
	}
	return f.Sync()
}
func (d durableFiles) syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return d.syncFile(f)
}
func syncDir(path string) error { return (durableFiles{}).syncDir(path) }

// durableWrite exposes only complete file bytes, then persists the directory
// entry. A failure after rename remains diagnosable to the caller.
func (d durableFiles) write(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = d.syncFile(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = d.renamePath(f.Name(), path); err != nil {
		return err
	}
	return d.syncDir(filepath.Dir(path))
}

func durableRemove(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}

func durableMkdir(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if err := durableMkdir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, userScopeDirMode); err != nil && !os.IsExist(err) {
		return err
	}
	return syncDir(parent)
}
