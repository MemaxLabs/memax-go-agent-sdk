package filetools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OSFS adapts a directory on the host filesystem to FileSystem.
//
// All paths are resolved relative to the configured root. Lexical path escapes
// such as ../secret are rejected before any host filesystem operation runs.
type OSFS struct {
	root string
}

func NewOSFS(root string) (*OSFS, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("filetools: OSFS root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filetools: resolve OSFS root: %w", err)
	}
	return &OSFS{root: filepath.Clean(abs)}, nil
}

func (fsys *OSFS) ReadFile(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	full, _, err := fsys.join(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (fsys *OSFS) WriteFile(ctx context.Context, name string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isInvalidWritePath(name) {
		return errInvalidPath(name)
	}
	full, _, err := fsys.join(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func (fsys *OSFS) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	full, clean, err := fsys.join(cleanPrefix(prefix))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return []string{clean}, nil
	}

	var files []string
	err = filepath.WalkDir(full, func(name string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fsys.root, name)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (fsys *OSFS) join(name string) (string, string, error) {
	clean := cleanPath(name)
	parts := strings.Split(clean, "/")
	full := filepath.Join(append([]string{fsys.root}, parts...)...)
	full = filepath.Clean(full)
	rel, err := filepath.Rel(fsys.root, full)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("filetools: path escapes workspace root: %s", name)
	}
	return full, clean, nil
}

// ReadOnlyFS adapts any io/fs.FS implementation to FileSystem.
type ReadOnlyFS struct {
	fsys fs.FS
}

func NewReadOnlyFS(fsys fs.FS) (*ReadOnlyFS, error) {
	if fsys == nil {
		return nil, fmt.Errorf("filetools: fs is required")
	}
	return &ReadOnlyFS{fsys: fsys}, nil
}

func (fsys *ReadOnlyFS) ReadFile(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := fsPath(name)
	if err != nil {
		return "", err
	}
	data, err := fs.ReadFile(fsys.fsys, name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (fsys *ReadOnlyFS) WriteFile(context.Context, string, string) error {
	return fmt.Errorf("filetools: read-only filesystem")
}

func (fsys *ReadOnlyFS) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	prefix, err := fsPath(cleanPrefix(prefix))
	if err != nil {
		return nil, err
	}
	info, err := fs.Stat(fsys.fsys, prefix)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return []string{prefix}, nil
	}

	var files []string
	err = fs.WalkDir(fsys.fsys, prefix, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func fsPath(name string) (string, error) {
	clean := cleanPath(name)
	if clean == "." {
		return ".", nil
	}
	if !fs.ValidPath(clean) {
		return "", fmt.Errorf("filetools: invalid fs path: %s", name)
	}
	return clean, nil
}
