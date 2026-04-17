package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultOSStoreDirMode  fs.FileMode = 0o755
	defaultOSStoreFileMode fs.FileMode = 0o644
)

// OSStore adapts a host directory to Store. It keeps the SDK boundary
// workspace-relative and forward-slash based while translating to host paths
// internally.
//
// Symlink containment is enabled by default. Existing paths and deepest
// existing parents for new paths are resolved with filepath.EvalSymlinks and
// must remain inside the configured root.
//
// OSStore is a reference adapter, not a transactional filesystem. Its mutex
// protects concurrent SDK calls through the store, but it cannot prevent
// external processes from changing the same directory. Checkpoints are in-memory
// snapshots and patch/diff/restore operations read the full workspace content.
// Restore attempts rollback after I/O failures, but filesystem failures such as
// disk-full or permission errors can still leave partial writes.
type OSStore struct {
	mu              sync.Mutex
	root            string
	dirMode         fs.FileMode
	fileMode        fs.FileMode
	containSymlinks bool
	checkpoints     map[string]snapshot
	order           []string
	next            int
	baseID          string
}

// OSStoreOption configures an OSStore.
type OSStoreOption func(*OSStore)

// WithOSStoreSymlinkContainment configures symlink containment checks. It is
// enabled by default; disabling it is intended only for externally sandboxed
// environments.
func WithOSStoreSymlinkContainment(enabled bool) OSStoreOption {
	return func(store *OSStore) {
		store.containSymlinks = enabled
	}
}

// WithOSStoreModes sets modes used for newly created directories and files.
func WithOSStoreModes(dirMode fs.FileMode, fileMode fs.FileMode) OSStoreOption {
	return func(store *OSStore) {
		if dirMode != 0 {
			store.dirMode = dirMode
		}
		if fileMode != 0 {
			store.fileMode = fileMode
		}
	}
}

// NewOSStore returns a root-confined workspace store backed by a host
// directory. The root must already exist when symlink containment is enabled.
func NewOSStore(root string, opts ...OSStoreOption) (*OSStore, error) {
	return newOSStore(root, true, opts...)
}

func newOSStore(root string, withInitialCheckpoint bool, opts ...OSStoreOption) (*OSStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("workspace: OSStore root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve OSStore root: %w", err)
	}
	store := &OSStore{
		root:            filepath.Clean(abs),
		dirMode:         defaultOSStoreDirMode,
		fileMode:        defaultOSStoreFileMode,
		containSymlinks: true,
		checkpoints:     map[string]snapshot{},
		next:            1,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	if store.containSymlinks {
		resolved, err := filepath.EvalSymlinks(store.root)
		if err != nil {
			return nil, fmt.Errorf("workspace: resolve OSStore root symlinks: %w", err)
		}
		store.root = filepath.Clean(resolved)
	}
	if !withInitialCheckpoint {
		return store, nil
	}
	files, err := store.readAllFiles(context.Background())
	if err != nil {
		return nil, err
	}
	cp := Checkpoint{
		ID:        "checkpoint-0",
		Label:     "initial",
		CreatedAt: time.Now().UTC(),
		Files:     len(files),
	}
	store.baseID = cp.ID
	store.checkpoints[cp.ID] = snapshot{Checkpoint: cp, files: files}
	store.order = append(store.order, cp.ID)
	return store, nil
}

// ReadFile returns the file content for path.
func (s *OSStore) ReadFile(ctx context.Context, name string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if s == nil {
		return "", fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFileLocked(name)
}

// WriteFile creates or replaces a file.
func (s *OSStore) WriteFile(ctx context.Context, name string, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeFileLocked(name, content)
}

// DeleteFile deletes a file.
func (s *OSStore) DeleteFile(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteFileLocked(name)
}

// ListFiles returns sorted file paths under prefix.
func (s *OSStore) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listFilesLocked(ctx, prefix)
}

// ApplyPatch applies all operations under the store lock. Validation is atomic
// against the snapshot OSStore reads before mutation. The final filesystem
// writes are best-effort; OSStore attempts to restore the previous snapshot if
// a write/delete fails, but external filesystem changes and I/O failures are
// outside the store's transactional boundary.
func (s *OSStore) ApplyPatch(ctx context.Context, ops []PatchOperation) (PatchResult, error) {
	return s.applyPatch(ctx, ops, PatchOptions{})
}

// PreviewPatch validates operations and returns the changes they would apply
// without mutating files.
func (s *OSStore) PreviewPatch(ctx context.Context, ops []PatchOperation) (PatchResult, error) {
	return s.applyPatch(ctx, ops, PatchOptions{DryRun: true})
}

// ApplyUnifiedDiff parses and applies a standard unified diff. The mutation is
// re-validated after parsing and review layers should treat the final apply as
// authoritative.
func (s *OSStore) ApplyUnifiedDiff(ctx context.Context, diff string, opts PatchOptions) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	if s == nil {
		return PatchResult{}, fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.readAllFilesLocked(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	ops, err := unifiedDiffOperations(diff, files)
	if err != nil {
		return PatchResult{}, err
	}
	return s.applyPatchLocked(ctx, ops, opts)
}

// Diff returns changes between a checkpoint and current files. Empty baseID
// compares against the initial checkpoint.
func (s *OSStore) Diff(ctx context.Context, baseID string) (Diff, error) {
	if err := contextError(ctx); err != nil {
		return Diff{}, err
	}
	if s == nil {
		return Diff{}, fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(baseID) == "" {
		baseID = s.baseID
	}
	base, ok := s.checkpoints[baseID]
	if !ok {
		return Diff{}, fmt.Errorf("workspace: checkpoint not found: %s", baseID)
	}
	current, err := s.readAllFilesLocked(ctx)
	if err != nil {
		return Diff{}, err
	}
	return Diff{BaseID: baseID, Changes: diffFiles(base.files, current)}, nil
}

// Checkpoint snapshots current file content in memory.
func (s *OSStore) Checkpoint(ctx context.Context, opts CheckpointOptions) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.readAllFilesLocked(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{
		ID:        s.nextIDLocked(),
		Label:     strings.TrimSpace(opts.Label),
		CreatedAt: time.Now().UTC(),
		Files:     len(files),
		Metadata:  cloneMetadata(opts.Metadata),
	}
	s.checkpoints[cp.ID] = snapshot{Checkpoint: cp, files: files}
	s.order = append(s.order, cp.ID)
	return cloneCheckpoint(cp), nil
}

// Restore resets current files to a checkpoint snapshot. Restore is best-effort
// on OS-backed filesystems: if deleting or rewriting files fails, OSStore
// attempts to roll back to the pre-restore snapshot, but rollback can also fail
// under the same I/O condition.
func (s *OSStore) Restore(ctx context.Context, id string) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil OSStore")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Checkpoint{}, fmt.Errorf("workspace: checkpoint id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.checkpoints[id]
	if !ok {
		return Checkpoint{}, fmt.Errorf("workspace: checkpoint not found: %s", id)
	}
	current, err := s.readAllFilesLocked(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := s.restoreFilesLocked(ctx, cp.files); err != nil {
		_ = s.restoreFilesLocked(context.Background(), current)
		return Checkpoint{}, err
	}
	return cloneCheckpoint(cp.Checkpoint), nil
}

// ListCheckpoints returns checkpoints sorted by creation order.
func (s *OSStore) ListCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil OSStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Checkpoint, 0, len(s.order))
	for _, id := range s.order {
		if cp, ok := s.checkpoints[id]; ok {
			out = append(out, cloneCheckpoint(cp.Checkpoint))
		}
	}
	return out, nil
}

func (s *OSStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("checkpoint-%d", s.next)
		s.next++
		if _, ok := s.checkpoints[id]; !ok {
			return id
		}
	}
}

func (s *OSStore) applyPatch(ctx context.Context, ops []PatchOperation, opts PatchOptions) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	if s == nil {
		return PatchResult{}, fmt.Errorf("workspace: nil OSStore")
	}
	if len(ops) == 0 {
		return PatchResult{}, fmt.Errorf("workspace: patch requires at least one operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyPatchLocked(ctx, ops, opts)
}

func (s *OSStore) applyPatchLocked(ctx context.Context, ops []PatchOperation, opts PatchOptions) (PatchResult, error) {
	if len(ops) == 0 {
		return PatchResult{}, fmt.Errorf("workspace: patch requires at least one operation")
	}
	current, err := s.readAllFilesLocked(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	result, err := applyPatchToFiles(current, ops, PatchOptions{DryRun: true})
	if err != nil || opts.DryRun {
		result.DryRun = opts.DryRun
		return result, err
	}
	if err := s.restoreFilesLocked(context.Background(), currentAfter(result.Changes, current)); err != nil {
		_ = s.restoreFilesLocked(context.Background(), current)
		return PatchResult{}, err
	}
	return PatchResult{Changes: result.Changes}, nil
}

func currentAfter(changes []Change, current map[string]string) map[string]string {
	next := cloneFiles(current)
	for _, change := range changes {
		if change.Kind == ChangeDeleted {
			delete(next, change.Path)
			continue
		}
		next[change.Path] = change.After
	}
	return next
}

func (s *OSStore) restoreFilesLocked(ctx context.Context, files map[string]string) error {
	current, err := s.listFilesLocked(ctx, "")
	if err != nil {
		return err
	}
	for _, name := range current {
		if err := s.deleteFileLocked(name); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.writeFileLocked(name, files[name]); err != nil {
			return err
		}
	}
	return nil
}

func (s *OSStore) readAllFiles(ctx context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readAllFilesLocked(ctx)
}

func (s *OSStore) readAllFilesLocked(ctx context.Context) (map[string]string, error) {
	names, err := s.listFilesLocked(ctx, "")
	if err != nil {
		return nil, err
	}
	files := make(map[string]string, len(names))
	for _, name := range names {
		content, err := s.readFileLocked(name)
		if err != nil {
			return nil, err
		}
		files[name] = content
	}
	return files, nil
}

func (s *OSStore) readFileLocked(name string) (string, error) {
	full, _, err := s.join(name)
	if err != nil {
		return "", err
	}
	full, err = s.resolveExisting(full)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *OSStore) writeFileLocked(name string, content string) error {
	full, _, err := s.join(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), s.dirMode); err != nil {
		return err
	}
	full, err = s.resolveWriteTarget(full)
	if err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), s.fileMode)
}

func (s *OSStore) deleteFileLocked(name string) error {
	full, _, err := s.join(name)
	if err != nil {
		return err
	}
	if s.containSymlinks {
		if _, err := s.resolveExisting(full); err != nil {
			return err
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("workspace: cannot delete directory: %s", name)
	}
	return os.Remove(full)
}

func (s *OSStore) listFilesLocked(ctx context.Context, prefix string) ([]string, error) {
	full, clean, err := s.join(cleanPrefix(prefix))
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(full); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	full, err = s.resolveExisting(full)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{clean}, nil
	}
	var files []string
	err = filepath.WalkDir(full, func(name string, entry os.DirEntry, walkErr error) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if _, err := s.resolveExisting(name); err != nil {
			return err
		}
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, name)
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

func (s *OSStore) join(name string) (string, string, error) {
	clean, err := cleanWorkspacePathStrict(name)
	if err != nil {
		return "", "", err
	}
	if clean == "." {
		return s.root, "", nil
	}
	full := filepath.Join(append([]string{s.root}, strings.Split(clean, "/")...)...)
	full = filepath.Clean(full)
	if err := s.ensureContained(full); err != nil {
		return "", "", err
	}
	return full, clean, nil
}

func (s *OSStore) resolveExisting(full string) (string, error) {
	if !s.containSymlinks {
		return full, nil
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if err := s.ensureContained(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (s *OSStore) resolveWriteTarget(full string) (string, error) {
	if !s.containSymlinks {
		return full, nil
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		if err := s.ensureContained(resolved); err != nil {
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
	if err := s.ensureContained(parent); err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(full)), nil
}

func (s *OSStore) ensureContained(candidate string) error {
	rel, err := filepath.Rel(s.root, filepath.Clean(candidate))
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("workspace: path escapes workspace root")
	}
	return nil
}

func cleanWorkspacePathStrict(name string) (string, error) {
	original := strings.TrimSpace(name)
	if original == "" {
		return ".", nil
	}
	if strings.Contains(original, `\`) {
		return "", fmt.Errorf("workspace: invalid file path %q: use forward slashes", name)
	}
	if strings.HasPrefix(original, "/") || filepath.IsAbs(original) {
		return "", fmt.Errorf("workspace: invalid file path: %s", name)
	}
	clean := cleanPath(original)
	if invalidPath(clean) {
		return "", fmt.Errorf("workspace: invalid file path: %s", name)
	}
	return clean, nil
}
