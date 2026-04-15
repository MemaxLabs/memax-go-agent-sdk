package filetools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultOSFSDirMode  fs.FileMode = 0o755
	defaultOSFSFileMode fs.FileMode = 0o644
)

// OSFS adapts a directory on the host filesystem to FileSystem.
//
// All paths are resolved relative to the configured root. Lexical path escapes
// such as ../secret are rejected before any host filesystem operation runs.
type OSFS struct {
	root            string
	containSymlinks bool
	maxReadBytes    int64
	maxListEntries  int
	dirMode         fs.FileMode
	fileMode        fs.FileMode
}

// OSFSOption configures an OSFS.
type OSFSOption func(*OSFS)

// WithSymlinkContainment verifies that symlink-resolved paths stay under the
// configured root before reading, writing, or listing.
func WithSymlinkContainment(enabled bool) OSFSOption {
	return func(fsys *OSFS) {
		fsys.containSymlinks = enabled
	}
}

// WithMaxReadBytes rejects reads whose file size is larger than n bytes.
func WithMaxReadBytes(n int64) OSFSOption {
	return func(fsys *OSFS) {
		fsys.maxReadBytes = n
	}
}

// WithMaxListEntries rejects list operations after more than n files are found.
func WithMaxListEntries(n int) OSFSOption {
	return func(fsys *OSFS) {
		fsys.maxListEntries = n
	}
}

// WithModes sets the modes used for newly created directories and files.
func WithModes(dirMode fs.FileMode, fileMode fs.FileMode) OSFSOption {
	return func(fsys *OSFS) {
		if dirMode != 0 {
			fsys.dirMode = dirMode
		}
		if fileMode != 0 {
			fsys.fileMode = fileMode
		}
	}
}

func NewOSFS(root string, opts ...OSFSOption) (*OSFS, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("filetools: OSFS root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filetools: resolve OSFS root: %w", err)
	}
	fsys := &OSFS{
		root:     filepath.Clean(abs),
		dirMode:  defaultOSFSDirMode,
		fileMode: defaultOSFSFileMode,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(fsys)
		}
	}
	if fsys.containSymlinks {
		resolved, err := filepath.EvalSymlinks(fsys.root)
		if err != nil {
			return nil, fmt.Errorf("filetools: resolve OSFS root symlinks: %w", err)
		}
		fsys.root = filepath.Clean(resolved)
	}
	return fsys, nil
}

func (fsys *OSFS) ReadFile(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	full, _, err := fsys.join(name)
	if err != nil {
		return "", err
	}
	full, err = fsys.resolveExisting(full)
	if err != nil {
		return "", err
	}
	if fsys.maxReadBytes > 0 {
		info, err := os.Stat(full)
		if err != nil {
			return "", err
		}
		if info.Size() > fsys.maxReadBytes {
			return "", fmt.Errorf("filetools: file exceeds max read bytes: %s", name)
		}
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
	if err := os.MkdirAll(filepath.Dir(full), fsys.dirMode); err != nil {
		return err
	}
	full, err = fsys.resolveWriteTarget(full)
	if err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), fsys.fileMode)
}

func (fsys *OSFS) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	full, clean, err := fsys.join(cleanPrefix(prefix))
	if err != nil {
		return nil, err
	}
	full, err = fsys.resolveExisting(full)
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
		if fsys.maxListEntries > 0 && len(files) > fsys.maxListEntries {
			return fmt.Errorf("filetools: list exceeds max entries: %d", fsys.maxListEntries)
		}
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

func (fsys *OSFS) resolveExisting(full string) (string, error) {
	if !fsys.containSymlinks {
		return full, nil
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if err := fsys.ensureContained(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (fsys *OSFS) resolveWriteTarget(full string) (string, error) {
	if !fsys.containSymlinks {
		return full, nil
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		if err := fsys.ensureContained(resolved); err != nil {
			return "", err
		}
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return "", err
	}
	if err := fsys.ensureContained(parent); err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(full)), nil
}

func (fsys *OSFS) ensureContained(path string) error {
	rel, err := filepath.Rel(fsys.root, filepath.Clean(path))
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("filetools: path escapes workspace root after symlink resolution")
	}
	return nil
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
		if errors.Is(err, fs.ErrNotExist) {
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
