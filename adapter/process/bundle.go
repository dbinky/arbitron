package process

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
)

// MaxBundleBytes bounds uncompressed regular-file content per bundle.
const MaxBundleBytes int64 = 128 << 20

// ErrBundleTooLarge refuses folders exceeding the content cap.
var ErrBundleTooLarge = workflow.ErrBundleTooLarge

// ErrBundleOutsideReference refuses references outside the script folder.
var ErrBundleOutsideReference = workflow.ErrBundleOutsideReference

// ErrBundleUnsupportedFile refuses devices, sockets and FIFOs.
var ErrBundleUnsupportedFile = workflow.ErrBundleUnsupportedFile

// ErrBundleUnsafeEntry refuses unsafe archive paths and unsupported types.
var ErrBundleUnsafeEntry = errors.New("script bundle: unsafe archive entry")

func escapesRoot(linkDir, target string) bool {
	resolved := filepath.Clean(filepath.Join(linkDir, target))
	return filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator))
}

// BuildScriptBundle packs the script's folder into a content-addressed archive.
func BuildScriptBundle(scriptPath string) (engine.ScriptBundleRef, []byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	defer zw.Close()
	tw := tar.NewWriter(zw)
	defer tw.Close()
	err := walkScriptBundle(context.Background(), scriptPath, func(p string, hdr *tar.Header) error {
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return engine.ScriptBundleRef{}, nil, err
	}
	if err := tw.Close(); err != nil {
		return engine.ScriptBundleRef{}, nil, err
	}
	if err := zw.Close(); err != nil {
		return engine.ScriptBundleRef{}, nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return engine.ScriptBundleRef{Hash: hex.EncodeToString(sum[:]), Entrypoint: filepath.Base(scriptPath)}, buf.Bytes(), nil
}

// walkScriptBundle is shared by runtime bundling and owner-scoped validation.
func walkScriptBundle(ctx context.Context, scriptPath string, visit func(string, *tar.Header) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(scriptPath) {
		return fmt.Errorf("script bundle: path must be absolute: %q", scriptPath)
	}
	info, err := os.Lstat(scriptPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("script bundle: entrypoint must be a regular file: %q", scriptPath)
	}
	root := filepath.Dir(scriptPath)
	var total int64
	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if info.Size() > MaxBundleBytes-total {
				return fmt.Errorf("%w: %s", ErrBundleTooLarge, rel)
			}
			total += info.Size()
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrBundleUnsupportedFile, rel)
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeReg, Size: info.Size()}
		if entry.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			hdr.Typeflag, hdr.Size = tar.TypeSymlink, 0
			hdr.Linkname, err = os.Readlink(p)
			if err != nil {
				return err
			}
			if escapesRoot(filepath.Dir(rel), hdr.Linkname) {
				return fmt.Errorf("%w: %s -> %s", ErrBundleOutsideReference, rel, hdr.Linkname)
			}
		}
		if visit != nil {
			return visit(p, hdr)
		}
		return nil
	})
}

// ExtractScriptBundle restores the files and permissions in dst.
func ExtractScriptBundle(r io.Reader, dst string) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%w: %q", ErrBundleUnsafeEntry, hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeSymlink {
			return fmt.Errorf("%w: unsupported type %c", ErrBundleUnsafeEntry, hdr.Typeflag)
		}
		if hdr.Typeflag == tar.TypeSymlink && escapesRoot(filepath.Dir(filepath.FromSlash(name)), filepath.FromSlash(hdr.Linkname)) {
			return fmt.Errorf("%w: link %q -> %q", ErrBundleUnsafeEntry, name, hdr.Linkname)
		}
		if hdr.Typeflag == tar.TypeReg {
			if hdr.Size > MaxBundleBytes-total {
				return fmt.Errorf("%w: archive exceeds %d bytes", ErrBundleUnsafeEntry, MaxBundleBytes)
			}
			total += hdr.Size
		}
		target := filepath.FromSlash(name)
		if hdr.Typeflag == tar.TypeDir {
			if err := root.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("%w: %w", ErrBundleUnsafeEntry, err)
		}
		if hdr.Typeflag == tar.TypeSymlink {
			if err := root.Symlink(filepath.FromSlash(hdr.Linkname), target); err != nil {
				return err
			}
			continue
		}
		f, err := root.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBundleUnsafeEntry, err)
		}
		_, copyErr := io.CopyN(f, tr, hdr.Size)
		if copyErr == nil {
			copyErr = f.Chmod(os.FileMode(hdr.Mode).Perm())
		}
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
