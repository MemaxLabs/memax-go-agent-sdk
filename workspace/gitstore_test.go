package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStoreCheckpointDiffRestore(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, "old.txt"), "remove me")

	store, err := NewGitStore(root)
	if err != nil {
		t.Fatalf("NewGitStore returned error: %v", err)
	}
	created, err := store.Checkpoint(context.Background(), CheckpointOptions{
		Label:    "before",
		Metadata: map[string]any{"scope": "workspace"},
	})
	if err != nil {
		t.Fatalf("Checkpoint returned error: %v", err)
	}
	result, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "README.md", OldContent: StringPtr("hello"), NewContent: StringPtr("hello world")},
		{Path: "docs/new.md", NewContent: StringPtr("new")},
		{Path: "old.txt", OldContent: StringPtr("remove me")},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if got := summarizeChanges(result.Changes); got != "README.md:modified,docs/new.md:added,old.txt:deleted" {
		t.Fatalf("changes = %s", got)
	}
	diff, err := store.Diff(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if got := summarizeChanges(diff.Changes); got != "README.md:modified,docs/new.md:added,old.txt:deleted" {
		t.Fatalf("diff = %s", got)
	}
	if _, err := store.Restore(context.Background(), created.ID); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello" {
		t.Fatalf("README.md = %q, want restored content", content)
	}
	if _, err := store.ReadFile(context.Background(), "docs/new.md"); err == nil {
		t.Fatal("ReadFile docs/new.md returned nil, want restored deletion")
	}
	checkpoints, err := store.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("ListCheckpoints returned error: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Fatalf("checkpoints = %#v, want initial plus created checkpoint", checkpoints)
	}
	if checkpoints[1].ID != created.ID || checkpoints[1].Label != "before" {
		t.Fatalf("checkpoint[1] = %#v, want created checkpoint metadata", checkpoints[1])
	}
	if checkpoints[1].Metadata["scope"] != "workspace" {
		t.Fatalf("checkpoint metadata = %#v, want persisted metadata", checkpoints[1].Metadata)
	}
}

func TestGitStorePersistsCheckpointsAcrossReopen(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "README.md"), "version one")

	store, err := NewGitStore(root)
	if err != nil {
		t.Fatalf("NewGitStore returned error: %v", err)
	}
	created, err := store.Checkpoint(context.Background(), CheckpointOptions{
		Label:    "before patch",
		Metadata: map[string]any{"kind": "git-backed"},
	})
	if err != nil {
		t.Fatalf("Checkpoint returned error: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "README.md", OldContent: StringPtr("version one"), NewContent: StringPtr("version two")},
	}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}

	reopened, err := NewGitStore(root)
	if err != nil {
		t.Fatalf("NewGitStore reopen returned error: %v", err)
	}
	checkpoints, err := reopened.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("ListCheckpoints returned error: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Fatalf("checkpoints = %#v, want persisted initial plus explicit checkpoint", checkpoints)
	}
	if checkpoints[1].ID != created.ID || checkpoints[1].Label != "before patch" {
		t.Fatalf("checkpoint[1] = %#v, want persisted checkpoint details", checkpoints[1])
	}
	if checkpoints[1].Metadata["kind"] != "git-backed" {
		t.Fatalf("checkpoint metadata = %#v, want persisted metadata", checkpoints[1].Metadata)
	}
	if _, err := reopened.Restore(context.Background(), created.ID); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("os.ReadFile returned error: %v", err)
	}
	if string(content) != "version one" {
		t.Fatalf("README.md = %q, want restored version one", content)
	}
}

func TestGitStoreDoesNotCreateUnusedOSBaseline(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "README.md"), "hello")

	store, err := NewGitStore(root)
	if err != nil {
		t.Fatalf("NewGitStore returned error: %v", err)
	}
	if len(store.osStore.order) != 0 {
		t.Fatalf("osStore.order = %#v, want no embedded OS checkpoint baseline", store.osStore.order)
	}
	if len(store.osStore.checkpoints) != 0 {
		t.Fatalf("osStore.checkpoints = %#v, want no embedded OS checkpoint baseline", store.osStore.checkpoints)
	}
}

func TestGitStoreSubdirectoryNamespacesAreDistinct(t *testing.T) {
	ensureGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	leftRoot := filepath.Join(repo, "services", "api")
	rightRoot := filepath.Join(repo, "services", "worker")
	writeFile(t, filepath.Join(leftRoot, "README.md"), "api")
	writeFile(t, filepath.Join(rightRoot, "README.md"), "worker")

	left, err := NewGitStore(leftRoot)
	if err != nil {
		t.Fatalf("NewGitStore(left) returned error: %v", err)
	}
	right, err := NewGitStore(rightRoot)
	if err != nil {
		t.Fatalf("NewGitStore(right) returned error: %v", err)
	}
	if left.refNamespace == right.refNamespace {
		t.Fatalf("ref namespaces = %q and %q, want distinct namespaces", left.refNamespace, right.refNamespace)
	}
	leftCheckpoint, err := left.Checkpoint(context.Background(), CheckpointOptions{Label: "left"})
	if err != nil {
		t.Fatalf("left.Checkpoint returned error: %v", err)
	}
	rightCheckpoint, err := right.Checkpoint(context.Background(), CheckpointOptions{Label: "right"})
	if err != nil {
		t.Fatalf("right.Checkpoint returned error: %v", err)
	}
	leftCheckpoints, err := left.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("left.ListCheckpoints returned error: %v", err)
	}
	rightCheckpoints, err := right.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("right.ListCheckpoints returned error: %v", err)
	}
	if len(leftCheckpoints) != 2 || leftCheckpoints[1].ID != leftCheckpoint.ID {
		t.Fatalf("left checkpoints = %#v, want left checkpoint only", leftCheckpoints)
	}
	if len(rightCheckpoints) != 2 || rightCheckpoints[1].ID != rightCheckpoint.ID {
		t.Fatalf("right checkpoints = %#v, want right checkpoint only", rightCheckpoints)
	}
}

func TestGitStoreFiltersGitInternalsAnywhereInWorkspace(t *testing.T) {
	ensureGit(t)
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, "pkg", "submodule", ".git", "config"), "ignored")

	store, err := NewGitStore(root)
	if err != nil {
		t.Fatalf("NewGitStore returned error: %v", err)
	}
	files, err := store.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if got := strings.Join(files, ","); got != "README.md" {
		t.Fatalf("ListFiles = %q, want only README.md", got)
	}
	if err := store.WriteFile(context.Background(), ".git/HEAD", "ref: refs/heads/main\n"); err == nil {
		t.Fatal("WriteFile(.git/HEAD) returned nil, want error")
	}
	if err := store.WriteFile(context.Background(), "pkg/submodule/.git/config", "ignored"); err == nil {
		t.Fatal("WriteFile(pkg/submodule/.git/config) returned nil, want error")
	}
	if _, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: ".git/HEAD", NewContent: StringPtr("ref: refs/heads/main\n")},
	}); err == nil {
		t.Fatal("ApplyPatch(.git/HEAD) returned nil, want error")
	}
}

func ensureGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func runGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s returned error: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, name string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}
