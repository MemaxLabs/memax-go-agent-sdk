// Package workspace defines source-neutral workspace state, patch, diff, and
// checkpoint contracts for optional coding-agent toolkits.
package workspace

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChangeKind describes how one workspace path differs from a checkpoint.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeModified ChangeKind = "modified"
	ChangeDeleted  ChangeKind = "deleted"
)

// Change is one file-level difference between a checkpoint and current state.
type Change struct {
	Path   string
	Kind   ChangeKind
	Before string
	After  string
}

// Diff is a deterministic list of workspace changes.
type Diff struct {
	BaseID  string
	Changes []Change
}

// PatchOperation is one guarded file mutation. OldContent is optional; when it
// is non-nil the current file content must match before the mutation is
// applied. NewContent nil deletes the file.
type PatchOperation struct {
	Path       string
	OldContent *string
	NewContent *string
}

// PatchResult describes the changes applied by ApplyPatch.
type PatchResult struct {
	Changes []Change
}

// Checkpoint is a restorable snapshot of workspace state.
type Checkpoint struct {
	ID        string
	Label     string
	CreatedAt time.Time
	Files     int
	Metadata  map[string]any
}

// CheckpointOptions configures Checkpoint creation.
type CheckpointOptions struct {
	Label    string
	Metadata map[string]any
}

// Store is the full source-neutral workspace capability contract. Hosts can
// expose narrower capability subsets by wrapping this store with only the tools
// they want the model to use.
type Store interface {
	ReadFile(context.Context, string) (string, error)
	WriteFile(context.Context, string, string) error
	DeleteFile(context.Context, string) error
	ListFiles(context.Context, string) ([]string, error)
	ApplyPatch(context.Context, []PatchOperation) (PatchResult, error)
	Diff(context.Context, string) (Diff, error)
	Checkpoint(context.Context, CheckpointOptions) (Checkpoint, error)
	Restore(context.Context, string) (Checkpoint, error)
	ListCheckpoints(context.Context) ([]Checkpoint, error)
}

// MemoryStore is a concurrency-safe in-memory Store for tests, examples, and
// short-lived agents. It is also the reference implementation for the workspace
// contract; production hosts can implement Store over git, databases, remote
// sandboxes, or object snapshots.
type MemoryStore struct {
	mu          sync.RWMutex
	files       map[string]string
	checkpoints map[string]snapshot
	order       []string
	next        int
	baseID      string
}

type snapshot struct {
	Checkpoint
	files map[string]string
}

// NewMemoryStore returns an in-memory workspace seeded with files and an
// implicit initial checkpoint used as the default diff base.
func NewMemoryStore(files map[string]string) *MemoryStore {
	store := &MemoryStore{
		files:       make(map[string]string, len(files)),
		checkpoints: make(map[string]snapshot),
		next:        1,
	}
	for name, content := range files {
		clean := cleanPath(name)
		if clean != "." {
			store.files[clean] = content
		}
	}
	cp := Checkpoint{
		ID:        "checkpoint-0",
		Label:     "initial",
		CreatedAt: time.Now().UTC(),
		Files:     len(store.files),
	}
	store.baseID = cp.ID
	store.checkpoints[cp.ID] = snapshot{Checkpoint: cp, files: cloneFiles(store.files)}
	store.order = append(store.order, cp.ID)
	return store
}

// ReadFile returns the file content for path.
func (s *MemoryStore) ReadFile(ctx context.Context, name string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if s == nil {
		return "", fmt.Errorf("workspace: nil MemoryStore")
	}
	name = cleanPath(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	content, ok := s.files[name]
	if !ok {
		return "", fmt.Errorf("workspace: file not found: %s", name)
	}
	return content, nil
}

// WriteFile creates or replaces a file.
func (s *MemoryStore) WriteFile(ctx context.Context, name string, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil MemoryStore")
	}
	name = cleanPath(name)
	if invalidPath(name) {
		return fmt.Errorf("workspace: invalid file path: %s", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = content
	return nil
}

// DeleteFile deletes a file.
func (s *MemoryStore) DeleteFile(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil MemoryStore")
	}
	name = cleanPath(name)
	if invalidPath(name) {
		return fmt.Errorf("workspace: invalid file path: %s", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[name]; !ok {
		return fmt.Errorf("workspace: file not found: %s", name)
	}
	delete(s.files, name)
	return nil
}

// ListFiles returns sorted file paths under prefix.
func (s *MemoryStore) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil MemoryStore")
	}
	prefix = cleanPrefix(prefix)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var names []string
	for name := range s.files {
		if prefix == "" || name == prefix || strings.HasPrefix(name, prefix+"/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ApplyPatch applies all operations atomically. If any operation fails its
// old-content guard or validation, no files are changed.
func (s *MemoryStore) ApplyPatch(ctx context.Context, ops []PatchOperation) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	if s == nil {
		return PatchResult{}, fmt.Errorf("workspace: nil MemoryStore")
	}
	if len(ops) == 0 {
		return PatchResult{}, fmt.Errorf("workspace: patch requires at least one operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneFiles(s.files)
	changes := make([]Change, 0, len(ops))
	for _, op := range ops {
		name := cleanPath(op.Path)
		if invalidPath(name) {
			return PatchResult{}, fmt.Errorf("workspace: invalid file path: %s", op.Path)
		}
		before, exists := next[name]
		if op.OldContent != nil {
			if !exists {
				return PatchResult{}, fmt.Errorf("workspace: patch guard failed for %s: file does not exist", name)
			}
			if before != *op.OldContent {
				return PatchResult{}, fmt.Errorf("workspace: patch guard failed for %s: content mismatch", name)
			}
		}
		if op.NewContent == nil {
			if !exists {
				return PatchResult{}, fmt.Errorf("workspace: cannot delete missing file: %s", name)
			}
			delete(next, name)
			changes = append(changes, Change{Path: name, Kind: ChangeDeleted, Before: before})
			continue
		}
		after := *op.NewContent
		kind := ChangeAdded
		if exists {
			kind = ChangeModified
		}
		next[name] = after
		changes = append(changes, Change{Path: name, Kind: kind, Before: before, After: after})
	}
	s.files = next
	return PatchResult{Changes: changes}, nil
}

// Diff returns changes between a checkpoint and current state. Empty baseID
// compares against the initial checkpoint.
func (s *MemoryStore) Diff(ctx context.Context, baseID string) (Diff, error) {
	if err := contextError(ctx); err != nil {
		return Diff{}, err
	}
	if s == nil {
		return Diff{}, fmt.Errorf("workspace: nil MemoryStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if strings.TrimSpace(baseID) == "" {
		baseID = s.baseID
	}
	base, ok := s.checkpoints[baseID]
	if !ok {
		return Diff{}, fmt.Errorf("workspace: checkpoint not found: %s", baseID)
	}
	return Diff{BaseID: baseID, Changes: diffFiles(base.files, s.files)}, nil
}

// Checkpoint snapshots current file content.
func (s *MemoryStore) Checkpoint(ctx context.Context, opts CheckpointOptions) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil MemoryStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := Checkpoint{
		ID:        s.nextIDLocked(),
		Label:     strings.TrimSpace(opts.Label),
		CreatedAt: time.Now().UTC(),
		Files:     len(s.files),
		Metadata:  cloneMetadata(opts.Metadata),
	}
	s.checkpoints[cp.ID] = snapshot{Checkpoint: cp, files: cloneFiles(s.files)}
	s.order = append(s.order, cp.ID)
	return cloneCheckpoint(cp), nil
}

// Restore resets current files to a checkpoint snapshot.
func (s *MemoryStore) Restore(ctx context.Context, id string) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil MemoryStore")
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
	s.files = cloneFiles(cp.files)
	return cloneCheckpoint(cp.Checkpoint), nil
}

// ListCheckpoints returns checkpoints sorted by creation order.
func (s *MemoryStore) ListCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil MemoryStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Checkpoint, 0, len(s.order))
	for _, id := range s.order {
		if cp, ok := s.checkpoints[id]; ok {
			out = append(out, cloneCheckpoint(cp.Checkpoint))
		}
	}
	return out, nil
}

func (s *MemoryStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("checkpoint-%d", s.next)
		s.next++
		if _, ok := s.checkpoints[id]; !ok {
			return id
		}
	}
}

func diffFiles(before, after map[string]string) []Change {
	seen := map[string]struct{}{}
	for name := range before {
		seen[name] = struct{}{}
	}
	for name := range after {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Change, 0)
	for _, name := range names {
		oldContent, oldOK := before[name]
		newContent, newOK := after[name]
		switch {
		case !oldOK && newOK:
			out = append(out, Change{Path: name, Kind: ChangeAdded, After: newContent})
		case oldOK && !newOK:
			out = append(out, Change{Path: name, Kind: ChangeDeleted, Before: oldContent})
		case oldOK && newOK && oldContent != newContent:
			out = append(out, Change{Path: name, Kind: ChangeModified, Before: oldContent, After: newContent})
		}
	}
	return out
}

func cleanPath(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "."
	}
	return path.Clean(name)
}

func cleanPrefix(prefix string) string {
	prefix = cleanPath(prefix)
	if prefix == "." {
		return ""
	}
	return prefix
}

func invalidPath(name string) bool {
	name = cleanPath(name)
	return name == "." || name == ".." || strings.HasPrefix(name, "../")
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func cloneFiles(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for name, content := range files {
		out[name] = content
	}
	return out
}

func cloneCheckpoint(cp Checkpoint) Checkpoint {
	cp.Metadata = cloneMetadata(cp.Metadata)
	return cp
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

// StringPtr returns a pointer to value. It is a small convenience for building
// PatchOperation values in tests and host adapters.
func StringPtr(value string) *string {
	return &value
}
