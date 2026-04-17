package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func TestWorkspaceStoreDelegatesToBackend(t *testing.T) {
	backend := newWorkspaceBackend(map[string]string{"README.md": "hello\n"})
	store := NewWorkspaceStore(backend)

	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello\n" {
		t.Fatalf("content = %q, want %q", content, "hello\n")
	}

	previewer, ok := store.(interface {
		PreviewPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error)
	})
	if !ok {
		t.Fatal("store does not expose PreviewPatch, want preview-capable adapter")
	}
	oldContent := "hello\n"
	newContent := "hello world\n"
	result, err := previewer.PreviewPatch(context.Background(), []workspace.PatchOperation{{
		Path:       "README.md",
		OldContent: &oldContent,
		NewContent: &newContent,
	}})
	if err != nil {
		t.Fatalf("PreviewPatch returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("PreviewPatch DryRun = false, want true")
	}

	patcher, ok := store.(interface {
		ApplyUnifiedDiff(context.Context, string, workspace.PatchOptions) (workspace.PatchResult, error)
	})
	if !ok {
		t.Fatal("store does not expose ApplyUnifiedDiff, want unified-diff-capable adapter")
	}
	if _, err := patcher.ApplyUnifiedDiff(context.Background(), "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+updated\n", workspace.PatchOptions{DryRun: true}); err != nil {
		t.Fatalf("ApplyUnifiedDiff returned error: %v", err)
	}
}

func TestWorkspaceStorePreviewUnsupported(t *testing.T) {
	store := NewWorkspaceStore(noPreviewWorkspace{store: workspace.NewMemoryStore(map[string]string{"README.md": "hello\n"})})
	if _, ok := store.(interface {
		PreviewPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error)
	}); ok {
		t.Fatal("store exposes PreviewPatch, want no preview capability")
	}
}

func TestWorkspaceStoreUnifiedDiffUnsupported(t *testing.T) {
	store := NewWorkspaceStore(noUnifiedDiffWorkspace{store: workspace.NewMemoryStore(map[string]string{"README.md": "hello\n"})})
	if _, ok := store.(interface {
		ApplyUnifiedDiff(context.Context, string, workspace.PatchOptions) (workspace.PatchResult, error)
	}); ok {
		t.Fatal("store exposes ApplyUnifiedDiff, want no unified diff capability")
	}
}

func TestCommandRunnerDelegatesToBackend(t *testing.T) {
	backend := &fakeCommandRunner{}
	runner := NewCommandRunner(backend)

	result, err := runner.RunCommand(context.Background(), commandtools.Request{
		SessionID: "session-1",
		Argv:      []string{"go", "test", "./..."},
		CWD:       "repo",
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if !reflect.DeepEqual(backend.lastRequest.Argv, []string{"go", "test", "./..."}) {
		t.Fatalf("backend argv = %#v, want request argv", backend.lastRequest.Argv)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("result.Stdout = %q, want %q", result.Stdout, "ok\n")
	}
}

func TestNewCommandRunnerNil(t *testing.T) {
	runner := NewCommandRunner(nil)
	_, err := runner.RunCommand(context.Background(), commandtools.Request{
		SessionID: "session-1",
		Argv:      []string{"echo", "hello"},
	})
	if err == nil {
		t.Fatal("RunCommand returned nil error, want backend required error")
	}
	if !strings.Contains(err.Error(), "command backend is required") {
		t.Fatalf("RunCommand error = %v, want backend required message", err)
	}
}

func TestNewSessionToolsIncludesOptionalCapabilities(t *testing.T) {
	tools, err := NewSessionTools(&fakeSessionBackend{})
	if err != nil {
		t.Fatalf("NewSessionTools returned error: %v", err)
	}
	want := []string{
		commandtools.StartToolName,
		commandtools.ReadOutputToolName,
		commandtools.StopToolName,
		commandtools.ListToolName,
		commandtools.WriteInputToolName,
		commandtools.ResizeToolName,
	}
	if got := toolNames(tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %#v, want %#v", got, want)
	}
}

func TestNewSessionToolsOmitsUnsupportedOptionalCapabilities(t *testing.T) {
	tools, err := NewSessionTools(basicSessionBackend{})
	if err != nil {
		t.Fatalf("NewSessionTools returned error: %v", err)
	}
	want := []string{
		commandtools.StartToolName,
		commandtools.ReadOutputToolName,
		commandtools.StopToolName,
		commandtools.ListToolName,
	}
	if got := toolNames(tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %#v, want %#v", got, want)
	}
}

func TestNewSessionToolsNil(t *testing.T) {
	if _, err := NewSessionTools(nil); err == nil {
		t.Fatal("NewSessionTools returned nil error, want backend required error")
	}
}

func TestSessionCleanupOptionsInvokesCleaner(t *testing.T) {
	backend := &fakeSessionBackend{}
	runner := hook.NewRunner(SessionCleanupOptions(backend)...)

	if errs := runner.SessionEnded(context.Background(), hook.SessionEndedInput{
		SessionID: "session-1",
		Reason:    hook.StopReasonResult,
	}); len(errs) > 0 {
		t.Fatalf("SessionEnded returned errors: %v", errs)
	}
	if backend.cleanedSessionID != "session-1" {
		t.Fatalf("cleaned session = %q, want %q", backend.cleanedSessionID, "session-1")
	}
}

func TestSessionCleanupOptionsNil(t *testing.T) {
	if opts := SessionCleanupOptions(nil); opts != nil {
		t.Fatalf("SessionCleanupOptions(nil) = %#v, want nil", opts)
	}
}

func TestSessionCleanupOptionsPropagatesError(t *testing.T) {
	wantErr := errors.New("cleanup failed")
	runner := hook.NewRunner(SessionCleanupOptions(errorCleaner{err: wantErr})...)
	errs := runner.SessionEnded(context.Background(), hook.SessionEndedInput{
		SessionID: "session-1",
		Reason:    hook.StopReasonError,
	})
	if len(errs) != 1 || !errors.Is(errs[0], wantErr) {
		t.Fatalf("SessionEnded errors = %v, want [%v]", errs, wantErr)
	}
}

func TestNewSessionCleanerNil(t *testing.T) {
	cleaner := NewSessionCleaner(nil)
	err := cleaner.CleanupSession(context.Background(), "session-1")
	if err == nil {
		t.Fatal("CleanupSession returned nil error, want backend required error")
	}
	if !strings.Contains(err.Error(), "session cleaner backend is required") {
		t.Fatalf("CleanupSession error = %v, want backend required message", err)
	}
}

func TestNewWorkspaceStoreNilBackend(t *testing.T) {
	store := NewWorkspaceStore(nil)
	_, err := store.ReadFile(context.Background(), "README.md")
	if err == nil {
		t.Fatal("ReadFile returned nil error, want backend required error")
	}
	if !strings.Contains(err.Error(), "workspace backend is required") {
		t.Fatalf("ReadFile error = %v, want backend required message", err)
	}
}

type workspaceBackend struct {
	store *workspace.MemoryStore
}

func newWorkspaceBackend(files map[string]string) *workspaceBackend {
	return &workspaceBackend{store: workspace.NewMemoryStore(files)}
}

func (b *workspaceBackend) ReadFile(ctx context.Context, path string) (string, error) {
	return b.store.ReadFile(ctx, path)
}

func (b *workspaceBackend) WriteFile(ctx context.Context, path string, content string) error {
	return b.store.WriteFile(ctx, path, content)
}

func (b *workspaceBackend) DeleteFile(ctx context.Context, path string) error {
	return b.store.DeleteFile(ctx, path)
}

func (b *workspaceBackend) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	return b.store.ListFiles(ctx, prefix)
}

func (b *workspaceBackend) ApplyPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	return b.store.ApplyPatch(ctx, ops)
}

func (b *workspaceBackend) PreviewPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	return b.store.PreviewPatch(ctx, ops)
}

func (b *workspaceBackend) ApplyUnifiedDiff(ctx context.Context, diff string, opts workspace.PatchOptions) (workspace.PatchResult, error) {
	return b.store.ApplyUnifiedDiff(ctx, diff, opts)
}

func (b *workspaceBackend) Diff(ctx context.Context, baseID string) (workspace.Diff, error) {
	return b.store.Diff(ctx, baseID)
}

func (b *workspaceBackend) Checkpoint(ctx context.Context, opts workspace.CheckpointOptions) (workspace.Checkpoint, error) {
	return b.store.Checkpoint(ctx, opts)
}

func (b *workspaceBackend) Restore(ctx context.Context, id string) (workspace.Checkpoint, error) {
	return b.store.Restore(ctx, id)
}

func (b *workspaceBackend) ListCheckpoints(ctx context.Context) ([]workspace.Checkpoint, error) {
	return b.store.ListCheckpoints(ctx)
}

type noPreviewWorkspace struct {
	store *workspace.MemoryStore
}

func (b noPreviewWorkspace) ReadFile(ctx context.Context, path string) (string, error) {
	return b.store.ReadFile(ctx, path)
}

func (b noPreviewWorkspace) WriteFile(ctx context.Context, path string, content string) error {
	return b.store.WriteFile(ctx, path, content)
}

func (b noPreviewWorkspace) DeleteFile(ctx context.Context, path string) error {
	return b.store.DeleteFile(ctx, path)
}

func (b noPreviewWorkspace) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	return b.store.ListFiles(ctx, prefix)
}

func (b noPreviewWorkspace) ApplyPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	return b.store.ApplyPatch(ctx, ops)
}

func (b noPreviewWorkspace) Diff(ctx context.Context, baseID string) (workspace.Diff, error) {
	return b.store.Diff(ctx, baseID)
}

func (b noPreviewWorkspace) Checkpoint(ctx context.Context, opts workspace.CheckpointOptions) (workspace.Checkpoint, error) {
	return b.store.Checkpoint(ctx, opts)
}

func (b noPreviewWorkspace) Restore(ctx context.Context, id string) (workspace.Checkpoint, error) {
	return b.store.Restore(ctx, id)
}

func (b noPreviewWorkspace) ListCheckpoints(ctx context.Context) ([]workspace.Checkpoint, error) {
	return b.store.ListCheckpoints(ctx)
}

type noUnifiedDiffWorkspace struct {
	store *workspace.MemoryStore
}

func (b noUnifiedDiffWorkspace) ReadFile(ctx context.Context, path string) (string, error) {
	return b.store.ReadFile(ctx, path)
}

func (b noUnifiedDiffWorkspace) WriteFile(ctx context.Context, path string, content string) error {
	return b.store.WriteFile(ctx, path, content)
}

func (b noUnifiedDiffWorkspace) DeleteFile(ctx context.Context, path string) error {
	return b.store.DeleteFile(ctx, path)
}

func (b noUnifiedDiffWorkspace) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	return b.store.ListFiles(ctx, prefix)
}

func (b noUnifiedDiffWorkspace) ApplyPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	return b.store.ApplyPatch(ctx, ops)
}

func (b noUnifiedDiffWorkspace) PreviewPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	return b.store.PreviewPatch(ctx, ops)
}

func (b noUnifiedDiffWorkspace) Diff(ctx context.Context, baseID string) (workspace.Diff, error) {
	return b.store.Diff(ctx, baseID)
}

func (b noUnifiedDiffWorkspace) Checkpoint(ctx context.Context, opts workspace.CheckpointOptions) (workspace.Checkpoint, error) {
	return b.store.Checkpoint(ctx, opts)
}

func (b noUnifiedDiffWorkspace) Restore(ctx context.Context, id string) (workspace.Checkpoint, error) {
	return b.store.Restore(ctx, id)
}

func (b noUnifiedDiffWorkspace) ListCheckpoints(ctx context.Context) ([]workspace.Checkpoint, error) {
	return b.store.ListCheckpoints(ctx)
}

type fakeCommandRunner struct {
	lastRequest commandtools.Request
}

func (r *fakeCommandRunner) RunCommand(_ context.Context, req commandtools.Request) (commandtools.Result, error) {
	r.lastRequest = req
	return commandtools.Result{
		Argv:            append([]string(nil), req.Argv...),
		CWD:             req.CWD,
		ExitCode:        0,
		Duration:        10 * time.Millisecond,
		Stdout:          "ok\n",
		Stderr:          "",
		TimedOut:        false,
		OutputTruncated: false,
	}, nil
}

type basicSessionBackend struct{}

func (basicSessionBackend) StartCommand(_ context.Context, req commandtools.StartRequest) (commandtools.CommandSession, error) {
	return commandtools.CommandSession{
		ID:        "proc-1",
		SessionID: req.SessionID,
		Argv:      append([]string(nil), req.Argv...),
		Status:    commandtools.SessionRunning,
		StartedAt: time.Now().UTC(),
	}, nil
}

func (basicSessionBackend) ReadCommandOutput(_ context.Context, req commandtools.ReadRequest) (commandtools.ReadResult, error) {
	return commandtools.ReadResult{
		Session: commandtools.CommandSession{
			ID:        req.ID,
			SessionID: req.SessionID,
			Status:    commandtools.SessionRunning,
			StartedAt: time.Now().UTC(),
		},
	}, nil
}

func (basicSessionBackend) StopCommand(_ context.Context, req commandtools.StopRequest) (commandtools.CommandSession, error) {
	return commandtools.CommandSession{
		ID:        req.ID,
		SessionID: req.SessionID,
		Status:    commandtools.SessionStopped,
		StartedAt: time.Now().UTC(),
	}, nil
}

func (basicSessionBackend) ListCommands(_ context.Context, req commandtools.ListRequest) ([]commandtools.CommandSession, error) {
	return []commandtools.CommandSession{{
		ID:        "proc-1",
		SessionID: req.SessionID,
		Status:    commandtools.SessionRunning,
		StartedAt: time.Now().UTC(),
	}}, nil
}

type fakeSessionBackend struct {
	basicSessionBackend
	cleanedSessionID string
}

func (f *fakeSessionBackend) WriteCommandInput(_ context.Context, req commandtools.WriteRequest) (commandtools.WriteResult, error) {
	return commandtools.WriteResult{
		InputBytes: len(req.Input),
		NextSeq:    1,
		Session: commandtools.CommandSession{
			ID:        req.ID,
			SessionID: req.SessionID,
			Status:    commandtools.SessionRunning,
			StartedAt: time.Now().UTC(),
		},
	}, nil
}

func (f *fakeSessionBackend) ResizeCommandTerminal(_ context.Context, req commandtools.ResizeRequest) (commandtools.CommandSession, error) {
	return commandtools.CommandSession{
		ID:        req.ID,
		SessionID: req.SessionID,
		Status:    commandtools.SessionRunning,
		TTY:       true,
		Cols:      req.Cols,
		Rows:      req.Rows,
		StartedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeSessionBackend) CleanupSession(_ context.Context, sessionID string) error {
	f.cleanedSessionID = sessionID
	return nil
}

type errorCleaner struct {
	err error
}

func (c errorCleaner) CleanupSession(_ context.Context, _ string) error {
	return c.err
}

func toolNames(tools []tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, entry := range tools {
		names = append(names, entry.Spec().Name)
	}
	return names
}
