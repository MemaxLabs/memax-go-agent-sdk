package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitStoreBinary       = "git"
	defaultGitStoreRefNamespace = "refs/memax/checkpoints"
	defaultGitStoreAuthorName   = "Memax"
	defaultGitStoreAuthorEmail  = "memax@local"
)

// GitStore adapts a git worktree to the full Store interface. File mutation,
// guarded patching, and rooted path containment reuse OSStore internals.
// Checkpoints, restore baselines, and diff baselines are backed by git refs
// under a private namespace, so checkpoints survive process restarts and can be
// listed again by reopening the same store. Checkpoint creation becomes durable
// when the private ref is updated; like OSStore, on-disk workspace edits and
// restore rollback remain best-effort under filesystem failures.
//
// GitStore snapshots only the configured workspace root. The root may be the
// repository root or a subdirectory inside a repository; checkpoints are
// namespaced per root so multiple GitStores can coexist in one repo without
// ref collisions.
//
// GitStore is a reference adapter, not a sandbox. It requires a usable git
// binary and a git repository on disk. Like OSStore, it serializes SDK calls
// with a mutex but cannot prevent external processes from changing the same
// files concurrently.
type GitStore struct {
	osStore *OSStore

	repoRoot     string
	gitBinary    string
	refNamespace string
	next         int
	baseID       string
}

type gitStoreConfig struct {
	osStoreOpts  []OSStoreOption
	gitBinary    string
	refNamespace string
}

type gitCheckpointPayload struct {
	Label    string         `json:"label,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Files    int            `json:"files,omitempty"`
}

// GitStoreOption configures a GitStore.
type GitStoreOption func(*gitStoreConfig)

// WithGitStoreBinary configures the git executable used by GitStore.
func WithGitStoreBinary(binary string) GitStoreOption {
	return func(cfg *gitStoreConfig) {
		if strings.TrimSpace(binary) != "" {
			cfg.gitBinary = strings.TrimSpace(binary)
		}
	}
}

// WithGitStoreRefNamespace configures the base ref namespace used for
// checkpoints. GitStore adds a stable per-root suffix automatically.
func WithGitStoreRefNamespace(namespace string) GitStoreOption {
	return func(cfg *gitStoreConfig) {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			cfg.refNamespace = namespace
		}
	}
}

// WithGitStoreSymlinkContainment configures rooted symlink containment for the
// underlying OS-backed file adapter. It is enabled by default.
func WithGitStoreSymlinkContainment(enabled bool) GitStoreOption {
	return func(cfg *gitStoreConfig) {
		cfg.osStoreOpts = append(cfg.osStoreOpts, WithOSStoreSymlinkContainment(enabled))
	}
}

// WithGitStoreModes sets directory and file modes used by the underlying
// OS-backed file adapter for newly created paths.
func WithGitStoreModes(dirMode fs.FileMode, fileMode fs.FileMode) GitStoreOption {
	return func(cfg *gitStoreConfig) {
		cfg.osStoreOpts = append(cfg.osStoreOpts, WithOSStoreModes(dirMode, fileMode))
	}
}

// NewGitStore returns a rooted workspace store backed by a git repository. The
// configured root must be inside an initialized repository. Root-relative files
// are edited directly on disk while checkpoints, restore, and diff baselines
// are persisted as git objects referenced under a private namespace.
func NewGitStore(root string, opts ...GitStoreOption) (*GitStore, error) {
	cfg := gitStoreConfig{
		gitBinary:    defaultGitStoreBinary,
		refNamespace: defaultGitStoreRefNamespace,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	osStore, err := newOSStore(root, false, cfg.osStoreOpts...)
	if err != nil {
		return nil, err
	}
	repoRoot, err := gitRepoRoot(context.Background(), cfg.gitBinary, osStore.root)
	if err != nil {
		return nil, err
	}
	store := &GitStore{
		osStore:      osStore,
		repoRoot:     repoRoot,
		gitBinary:    cfg.gitBinary,
		refNamespace: gitNamespaceForRoot(cfg.refNamespace, repoRoot, osStore.root),
	}
	ids, err := store.checkpointIDsLocked(context.Background())
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		store.next = 1
		if _, err := store.createCheckpointLocked(context.Background(), "checkpoint-0", CheckpointOptions{Label: "initial"}); err != nil {
			return nil, err
		}
		store.baseID = "checkpoint-0"
		return store, nil
	}
	store.baseID = ids[0]
	store.next = nextCheckpointNumber(ids)
	return store, nil
}

// ReadFile returns workspace file content while reserving git internals.
func (s *GitStore) ReadFile(ctx context.Context, name string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if s == nil {
		return "", fmt.Errorf("workspace: nil GitStore")
	}
	clean, err := validateGitWorkspacePath(name)
	if err != nil {
		return "", err
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	return s.osStore.readFileLocked(clean)
}

// WriteFile creates or replaces a workspace file while reserving git internals.
func (s *GitStore) WriteFile(ctx context.Context, name string, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil GitStore")
	}
	clean, err := validateGitWorkspacePath(name)
	if err != nil {
		return err
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	return s.osStore.writeFileLocked(clean, content)
}

// DeleteFile deletes a workspace file while reserving git internals.
func (s *GitStore) DeleteFile(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("workspace: nil GitStore")
	}
	clean, err := validateGitWorkspacePath(name)
	if err != nil {
		return err
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	return s.osStore.deleteFileLocked(clean)
}

// ListFiles returns sorted workspace-relative paths while hiding .git internals.
func (s *GitStore) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil GitStore")
	}
	if prefix != "" {
		clean, err := validateGitWorkspacePath(prefix)
		if err != nil {
			return nil, err
		}
		prefix = clean
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	return s.listWorkspaceFilesLocked(ctx, prefix)
}

// ApplyPatch applies guarded workspace edits while keeping git internals
// outside the mutable workspace surface.
func (s *GitStore) ApplyPatch(ctx context.Context, ops []PatchOperation) (PatchResult, error) {
	return s.applyWorkspacePatch(ctx, ops, PatchOptions{})
}

// PreviewPatch validates guarded operations without mutating workspace state.
func (s *GitStore) PreviewPatch(ctx context.Context, ops []PatchOperation) (PatchResult, error) {
	return s.applyWorkspacePatch(ctx, ops, PatchOptions{DryRun: true})
}

// ApplyUnifiedDiff applies a standard unified diff against workspace files
// while keeping git internals private to the adapter.
func (s *GitStore) ApplyUnifiedDiff(ctx context.Context, diff string, opts PatchOptions) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	if s == nil {
		return PatchResult{}, fmt.Errorf("workspace: nil GitStore")
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	files, err := s.readAllWorkspaceFilesLocked(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	ops, err := unifiedDiffOperations(diff, files)
	if err != nil {
		return PatchResult{}, err
	}
	if err := validateGitPatchPaths(ops); err != nil {
		return PatchResult{}, err
	}
	return s.applyWorkspacePatchLocked(ctx, ops, opts)
}

// Diff returns changes between a persisted git-backed checkpoint and current
// workspace files. Empty baseID compares against the initial checkpoint.
func (s *GitStore) Diff(ctx context.Context, baseID string) (Diff, error) {
	if err := contextError(ctx); err != nil {
		return Diff{}, err
	}
	if s == nil {
		return Diff{}, fmt.Errorf("workspace: nil GitStore")
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	if strings.TrimSpace(baseID) == "" {
		baseID = s.baseID
	}
	files, _, err := s.snapshotFilesLocked(ctx, baseID)
	if err != nil {
		return Diff{}, err
	}
	current, err := s.readAllWorkspaceFilesLocked(ctx)
	if err != nil {
		return Diff{}, err
	}
	return Diff{BaseID: baseID, Changes: diffFiles(files, current)}, nil
}

// Checkpoint snapshots current workspace files into git-backed object storage.
func (s *GitStore) Checkpoint(ctx context.Context, opts CheckpointOptions) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil GitStore")
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	id := fmt.Sprintf("checkpoint-%d", s.next)
	cp, err := s.createCheckpointLocked(ctx, id, opts)
	if err != nil {
		return Checkpoint{}, err
	}
	s.next++
	return cp, nil
}

// Restore resets current workspace files to a persisted git-backed checkpoint.
func (s *GitStore) Restore(ctx context.Context, id string) (Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return Checkpoint{}, err
	}
	if s == nil {
		return Checkpoint{}, fmt.Errorf("workspace: nil GitStore")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Checkpoint{}, fmt.Errorf("workspace: checkpoint id is required")
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	files, cp, err := s.snapshotFilesLocked(ctx, id)
	if err != nil {
		return Checkpoint{}, err
	}
	current, err := s.readAllWorkspaceFilesLocked(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := s.restoreWorkspaceFilesLocked(ctx, files); err != nil {
		_ = s.restoreWorkspaceFilesLocked(context.Background(), current)
		return Checkpoint{}, err
	}
	return cloneCheckpoint(cp), nil
}

// ListCheckpoints returns git-backed checkpoints in deterministic creation
// order for this workspace root.
func (s *GitStore) ListCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("workspace: nil GitStore")
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	ids, err := s.checkpointIDsLocked(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, 0, len(ids))
	for _, id := range ids {
		_, cp, err := s.snapshotFilesLocked(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, nil
}

func (s *GitStore) createCheckpointLocked(ctx context.Context, id string, opts CheckpointOptions) (Checkpoint, error) {
	files, err := s.readAllWorkspaceFilesLocked(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	now := time.Now().UTC()
	cp := Checkpoint{
		ID:        id,
		Label:     strings.TrimSpace(opts.Label),
		CreatedAt: now,
		Files:     len(files),
		Metadata:  cloneMetadata(opts.Metadata),
	}
	payload := gitCheckpointPayload{
		Label:    cp.Label,
		Metadata: cloneMetadata(cp.Metadata),
		Files:    cp.Files,
	}
	commitOID, err := s.writeSnapshotCommitLocked(ctx, files, payload, now)
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := s.gitOutputTrimmed(ctx, nil, "", "update-ref", s.refForID(id), commitOID); err != nil {
		return Checkpoint{}, err
	}
	if s.baseID == "" {
		s.baseID = id
	}
	return cloneCheckpoint(cp), nil
}

func (s *GitStore) writeSnapshotCommitLocked(ctx context.Context, files map[string]string, payload gitCheckpointPayload, now time.Time) (string, error) {
	treeOID, err := s.writeTreeLocked(ctx, files)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("workspace: encode git checkpoint payload: %w", err)
	}
	env := gitCommitEnv(now)
	return s.gitOutputTrimmed(ctx, env, "", "commit-tree", treeOID, "-m", "Memax workspace checkpoint", "-m", string(body))
}

func (s *GitStore) writeTreeLocked(ctx context.Context, files map[string]string) (string, error) {
	indexFile, err := os.CreateTemp("", "memax-git-index-*")
	if err != nil {
		return "", fmt.Errorf("workspace: create git index: %w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", fmt.Errorf("workspace: close git index: %w", err)
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := s.gitOutputTrimmed(ctx, env, "", "read-tree", "--empty"); err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		blobOID, err := s.gitOutputTrimmed(ctx, env, files[name], "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		spec := "100644," + blobOID + "," + name
		if _, err := s.gitOutputTrimmed(ctx, env, "", "update-index", "--add", "--cacheinfo", spec); err != nil {
			return "", err
		}
	}
	return s.gitOutputTrimmed(ctx, env, "", "write-tree")
}

func (s *GitStore) snapshotFilesLocked(ctx context.Context, id string) (map[string]string, Checkpoint, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, Checkpoint{}, fmt.Errorf("workspace: checkpoint id is required")
	}
	ref := s.refForID(id)
	meta, err := s.lookupCheckpointLocked(ctx, ref, id)
	if err != nil {
		return nil, Checkpoint{}, err
	}
	namesRaw, err := s.gitOutputRaw(ctx, nil, "", "ls-tree", "-z", "-r", "--name-only", ref)
	if err != nil {
		return nil, Checkpoint{}, err
	}
	files := map[string]string{}
	if namesRaw == "" {
		return files, meta, nil
	}
	for _, name := range strings.Split(strings.TrimSuffix(namesRaw, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		content, err := s.gitOutputRaw(ctx, nil, "", "show", ref+":"+name)
		if err != nil {
			return nil, Checkpoint{}, err
		}
		files[name] = content
	}
	return files, meta, nil
}

func (s *GitStore) applyWorkspacePatch(ctx context.Context, ops []PatchOperation, opts PatchOptions) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	if s == nil {
		return PatchResult{}, fmt.Errorf("workspace: nil GitStore")
	}
	if err := validateGitPatchPaths(ops); err != nil {
		return PatchResult{}, err
	}
	s.osStore.mu.Lock()
	defer s.osStore.mu.Unlock()
	return s.applyWorkspacePatchLocked(ctx, ops, opts)
}

func (s *GitStore) applyWorkspacePatchLocked(ctx context.Context, ops []PatchOperation, opts PatchOptions) (PatchResult, error) {
	if len(ops) == 0 {
		return PatchResult{}, fmt.Errorf("workspace: patch requires at least one operation")
	}
	current, err := s.readAllWorkspaceFilesLocked(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	result, err := applyPatchToFiles(current, ops, PatchOptions{DryRun: true})
	if err != nil || opts.DryRun {
		result.DryRun = opts.DryRun
		return result, err
	}
	if err := s.restoreWorkspaceFilesLocked(context.Background(), currentAfter(result.Changes, current)); err != nil {
		_ = s.restoreWorkspaceFilesLocked(context.Background(), current)
		return PatchResult{}, err
	}
	return PatchResult{Changes: result.Changes}, nil
}

func (s *GitStore) readAllWorkspaceFilesLocked(ctx context.Context) (map[string]string, error) {
	names, err := s.listWorkspaceFilesLocked(ctx, "")
	if err != nil {
		return nil, err
	}
	files := make(map[string]string, len(names))
	for _, name := range names {
		content, err := s.osStore.readFileLocked(name)
		if err != nil {
			return nil, err
		}
		files[name] = content
	}
	return files, nil
}

func (s *GitStore) restoreWorkspaceFilesLocked(ctx context.Context, files map[string]string) error {
	current, err := s.listWorkspaceFilesLocked(ctx, "")
	if err != nil {
		return err
	}
	for _, name := range current {
		if err := s.osStore.deleteFileLocked(name); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.osStore.writeFileLocked(name, files[name]); err != nil {
			return err
		}
	}
	return nil
}

func (s *GitStore) listWorkspaceFilesLocked(ctx context.Context, prefix string) ([]string, error) {
	full, clean, err := s.osStore.join(cleanPrefix(prefix))
	if err != nil {
		return nil, err
	}
	if clean != "" && isGitInternalPath(clean) {
		return nil, fmt.Errorf("workspace: git internal path is not addressable: %s", prefix)
	}
	if _, err := os.Lstat(full); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	full, err = s.osStore.resolveExisting(full)
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
		rel, err := filepath.Rel(s.osStore.root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isGitInternalPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if _, err := s.osStore.resolveExisting(name); err != nil {
			return err
		}
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (s *GitStore) checkpointIDsLocked(ctx context.Context) ([]string, error) {
	out, err := s.gitOutputTrimmed(ctx, nil, "", "for-each-ref", "--format=%(refname)", s.refNamespace)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	ids := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ids = append(ids, pathBase(line))
	}
	sortCheckpointIDs(ids)
	return ids, nil
}

func (s *GitStore) lookupCheckpointLocked(ctx context.Context, ref string, id string) (Checkpoint, error) {
	out, err := s.gitOutputRaw(ctx, nil, "", "show", "-s", "--format=%ct%x00%B", ref)
	if err != nil {
		return Checkpoint{}, err
	}
	parts := strings.SplitN(out, "\x00", 2)
	if len(parts) != 2 {
		return Checkpoint{}, fmt.Errorf("workspace: invalid git checkpoint metadata for %s", id)
	}
	unixSeconds, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("workspace: parse git checkpoint timestamp for %s: %w", id, err)
	}
	payload, err := decodeGitCheckpointPayload(parts[1])
	if err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{
		ID:        id,
		Label:     payload.Label,
		CreatedAt: time.Unix(unixSeconds, 0).UTC(),
		Files:     payload.Files,
		// Metadata is persisted through JSON in git checkpoint payloads, so
		// numbers and other interface values come back in JSON-normalized forms.
		Metadata: cloneMetadata(payload.Metadata),
	}, nil
}

func decodeGitCheckpointPayload(message string) (gitCheckpointPayload, error) {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	parts := strings.SplitN(strings.TrimSpace(message), "\n\n", 2)
	if len(parts) != 2 {
		return gitCheckpointPayload{}, fmt.Errorf("workspace: invalid git checkpoint payload")
	}
	var payload gitCheckpointPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &payload); err != nil {
		return gitCheckpointPayload{}, fmt.Errorf("workspace: decode git checkpoint payload: %w", err)
	}
	return payload, nil
}

func (s *GitStore) refForID(id string) string {
	return strings.TrimRight(s.refNamespace, "/") + "/" + id
}

func (s *GitStore) gitOutputRaw(ctx context.Context, extraEnv []string, stdin string, args ...string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, s.gitBinary, append([]string{"-C", s.repoRoot}, args...)...)
	cmd.Env = append(scrubGitEnv(os.Environ()), extraEnv...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("workspace: git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func (s *GitStore) gitOutputTrimmed(ctx context.Context, extraEnv []string, stdin string, args ...string) (string, error) {
	out, err := s.gitOutputRaw(ctx, extraEnv, stdin, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

func gitRepoRoot(ctx context.Context, gitBinary string, root string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, "-C", root, "rev-parse", "--show-toplevel")
	cmd.Env = scrubGitEnv(os.Environ())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("workspace: resolve git repository root: %s", msg)
	}
	repoRoot := filepath.Clean(strings.TrimSpace(stdout.String()))
	if repoRoot == "" {
		return "", fmt.Errorf("workspace: resolve git repository root: empty output")
	}
	return repoRoot, nil
}

func gitNamespaceForRoot(base string, repoRoot string, root string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultGitStoreRefNamespace
	}
	rel, err := filepath.Rel(repoRoot, root)
	if err != nil || rel == "." {
		return base + "/root"
	}
	rel = filepath.ToSlash(rel)
	sum := sha256.Sum256([]byte(rel))
	return base + "/path-" + hex.EncodeToString(sum[:6])
}

func gitCommitEnv(now time.Time) []string {
	timestamp := fmt.Sprintf("%d +0000", now.UTC().Unix())
	return []string{
		"GIT_AUTHOR_NAME=" + defaultGitStoreAuthorName,
		"GIT_AUTHOR_EMAIL=" + defaultGitStoreAuthorEmail,
		"GIT_COMMITTER_NAME=" + defaultGitStoreAuthorName,
		"GIT_COMMITTER_EMAIL=" + defaultGitStoreAuthorEmail,
		"GIT_AUTHOR_DATE=" + timestamp,
		"GIT_COMMITTER_DATE=" + timestamp,
	}
}

func nextCheckpointNumber(ids []string) int {
	next := 1
	for _, id := range ids {
		if n, ok := checkpointNumber(id); ok && n >= next {
			next = n + 1
		}
	}
	return next
}

func sortCheckpointIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		leftN, leftOK := checkpointNumber(ids[i])
		rightN, rightOK := checkpointNumber(ids[j])
		switch {
		case leftOK && rightOK:
			return leftN < rightN
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return ids[i] < ids[j]
		}
	})
}

func checkpointNumber(id string) (int, bool) {
	if !strings.HasPrefix(id, "checkpoint-") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "checkpoint-"))
	if err != nil {
		return 0, false
	}
	return n, true
}

func pathBase(ref string) string {
	if idx := strings.LastIndexByte(ref, '/'); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
}

func validateGitPatchPaths(ops []PatchOperation) error {
	for _, op := range ops {
		if _, err := validateGitWorkspacePath(op.Path); err != nil {
			return err
		}
	}
	return nil
}

func validateGitWorkspacePath(name string) (string, error) {
	clean, err := cleanWorkspacePathStrict(name)
	if err != nil {
		return "", err
	}
	if isGitInternalPath(clean) {
		return "", fmt.Errorf("workspace: git internal path is not addressable: %s", name)
	}
	return clean, nil
}

func isGitInternalPath(name string) bool {
	if name == "" {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

func scrubGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		key := item
		if idx := strings.IndexByte(item, '='); idx >= 0 {
			key = item[:idx]
		}
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		out = append(out, item)
	}
	return out
}
