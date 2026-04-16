package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOSStorePatchDiffCheckpointRestore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store, err := NewOSStore(root)
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	cp, err := store.Checkpoint(context.Background(), CheckpointOptions{Label: "before"})
	if err != nil {
		t.Fatalf("Checkpoint returned error: %v", err)
	}
	result, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "README.md", OldContent: StringPtr("hello"), NewContent: StringPtr("hello world")},
		{Path: "docs/new.md", NewContent: StringPtr("new")},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if got := summarizeChanges(result.Changes); got != "README.md:modified,docs/new.md:added" {
		t.Fatalf("changes = %s", got)
	}
	diff, err := store.Diff(context.Background(), cp.ID)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if got := summarizeChanges(diff.Changes); got != "README.md:modified,docs/new.md:added" {
		t.Fatalf("diff = %s", got)
	}
	if _, err := store.Restore(context.Background(), cp.ID); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("README.md = %q, want restored hello", content)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/new.md")); !os.IsNotExist(err) {
		t.Fatalf("docs/new.md stat error = %v, want restored deletion", err)
	}
}

func TestOSStoreUnifiedDiffDryRunDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\nworld"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store, err := NewOSStore(root)
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	diff := `--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
 hello
-world
+workspace`
	result, err := store.ApplyUnifiedDiff(context.Background(), diff, PatchOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyUnifiedDiff dry-run returned error: %v", err)
	}
	if !result.DryRun || len(result.Changes) != 1 || result.Changes[0].After != "hello\nworkspace" {
		t.Fatalf("result = %#v, want dry-run preview", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "hello\nworld" {
		t.Fatalf("README.md = %q, want unchanged", content)
	}
}

func TestOSStoreRejectsPathEscapes(t *testing.T) {
	store, err := NewOSStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	for _, name := range []string{"../escape.txt", "/abs.txt", `dir\file.txt`} {
		if err := store.WriteFile(context.Background(), name, "nope"); err == nil {
			t.Fatalf("WriteFile %q returned nil, want invalid path", name)
		}
		if _, err := store.ReadFile(context.Background(), name); err == nil {
			t.Fatalf("ReadFile %q returned nil, want invalid path", name)
		}
	}
}

func TestOSStoreRejectsEmptyPatch(t *testing.T) {
	store, err := NewOSStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), nil); err == nil {
		t.Fatal("ApplyPatch returned nil, want empty patch error")
	}
	if _, err := store.PreviewPatch(context.Background(), nil); err == nil {
		t.Fatal("PreviewPatch returned nil, want empty patch error")
	}
}

func TestOSStoreSymlinkContainmentRejectsEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows setups")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	store, err := NewOSStore(root)
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := store.ReadFile(context.Background(), "outside/secret.txt"); err == nil {
		t.Fatal("ReadFile returned nil, want symlink escape error")
	}
	if err := store.WriteFile(context.Background(), "outside/new.txt", "nope"); err == nil {
		t.Fatal("WriteFile returned nil, want symlink escape error")
	}
}

func TestOSStoreDeletesContainedSymlinkNotTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows setups")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target"), 0o644); err != nil {
		t.Fatalf("write target fixture: %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	store, err := NewOSStore(root)
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	files, err := store.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if got := strings.Join(files, ","); got != "link.txt,target.txt" {
		t.Fatalf("files = %q, want lexical symlink and target paths", got)
	}
	if err := store.DeleteFile(context.Background(), "link.txt"); err != nil {
		t.Fatalf("DeleteFile returned error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link.txt")); !os.IsNotExist(err) {
		t.Fatalf("link stat error = %v, want deleted symlink", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "target.txt"))
	if err != nil {
		t.Fatalf("read target after symlink delete: %v", err)
	}
	if string(content) != "target" {
		t.Fatalf("target content = %q, want unchanged", content)
	}
}

func TestOSStorePatchRollbackOnGuardFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store, err := NewOSStore(root)
	if err != nil {
		t.Fatalf("NewOSStore returned error: %v", err)
	}
	_, err = store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "docs/new.md", NewContent: StringPtr("new")},
		{Path: "README.md", OldContent: StringPtr("wrong"), NewContent: StringPtr("changed")},
	})
	if err == nil || !strings.Contains(err.Error(), "content mismatch") {
		t.Fatalf("ApplyPatch error = %v, want guard failure", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/new.md")); !os.IsNotExist(err) {
		t.Fatalf("docs/new.md stat error = %v, want no partial write", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("README.md = %q, want unchanged", content)
	}
}
