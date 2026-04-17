// Package sandbox defines an optional host-owned execution substrate that can
// back workspace and command toolkits together.
//
// Leading coding agents commonly run against a fresh worktree, container, or
// remote executor rather than the user's raw machine. This package does not
// hard-code any transport or sandbox technology. Instead, it gives embedders a
// small integration seam for host-owned environments, then adapts those
// backends into the existing workspace and command toolkits.
package sandbox

import (
	"context"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

// Workspace is the baseline workspace surface exposed by a sandbox backend.
// Hosts may implement it over a remote executor, container, VM, or other
// application-owned environment.
type Workspace interface {
	workspace.Store
}

// WorkspacePreviewer is an optional extension for dry-run guarded patches.
type WorkspacePreviewer interface {
	PreviewPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error)
}

// WorkspaceUnifiedDiffPatcher is an optional extension for applying standard
// unified diffs directly inside the sandbox backend.
type WorkspaceUnifiedDiffPatcher interface {
	ApplyUnifiedDiff(context.Context, string, workspace.PatchOptions) (workspace.PatchResult, error)
}

// CommandRunner is the baseline one-shot command execution surface for a
// sandbox backend.
type CommandRunner interface {
	RunCommand(context.Context, commandtools.Request) (commandtools.Result, error)
}

// SessionManager is the baseline managed-command session surface for a sandbox
// backend.
type SessionManager interface {
	StartCommand(context.Context, commandtools.StartRequest) (commandtools.CommandSession, error)
	ReadCommandOutput(context.Context, commandtools.ReadRequest) (commandtools.ReadResult, error)
	StopCommand(context.Context, commandtools.StopRequest) (commandtools.CommandSession, error)
	ListCommands(context.Context, commandtools.ListRequest) ([]commandtools.CommandSession, error)
}

// SessionWriter is an optional extension for writing stdin to a running
// managed command session.
type SessionWriter interface {
	WriteCommandInput(context.Context, commandtools.WriteRequest) (commandtools.WriteResult, error)
}

// SessionResizer is an optional extension for PTY-backed terminal resize.
type SessionResizer interface {
	ResizeCommandTerminal(context.Context, commandtools.ResizeRequest) (commandtools.CommandSession, error)
}

// SessionCleaner is an optional extension for cleaning up managed command
// sessions when an agent session ends.
type SessionCleaner interface {
	CleanupSession(context.Context, string) error
}

// NewWorkspaceStore wraps backend so it can be passed to
// toolkit/workspacetools or any other consumer of workspace.Store. The
// returned concrete type exposes preview / unified-diff extensions only when
// the backend implements them.
func NewWorkspaceStore(backend Workspace) workspace.Store {
	base := &workspaceStore{backend: backend}
	previewer, hasPreview := any(backend).(WorkspacePreviewer)
	patcher, hasUnifiedDiff := any(backend).(WorkspaceUnifiedDiffPatcher)
	switch {
	case hasPreview && hasUnifiedDiff:
		return workspaceStoreWithPreviewAndUnifiedDiff{
			workspaceStore: base,
			previewer:      previewer,
			patcher:        patcher,
		}
	case hasPreview:
		return workspaceStoreWithPreview{
			workspaceStore: base,
			previewer:      previewer,
		}
	case hasUnifiedDiff:
		return workspaceStoreWithUnifiedDiff{
			workspaceStore: base,
			patcher:        patcher,
		}
	default:
		return base
	}
}

type workspaceStore struct {
	backend Workspace
}

func (s *workspaceStore) ReadFile(ctx context.Context, path string) (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.ReadFile(ctx, path)
}

func (s *workspaceStore) WriteFile(ctx context.Context, path string, content string) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.WriteFile(ctx, path, content)
}

func (s *workspaceStore) DeleteFile(ctx context.Context, path string) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.DeleteFile(ctx, path)
}

func (s *workspaceStore) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.ListFiles(ctx, prefix)
}

func (s *workspaceStore) ApplyPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	if s == nil || s.backend == nil {
		return workspace.PatchResult{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.ApplyPatch(ctx, ops)
}

func (s *workspaceStore) Diff(ctx context.Context, baseID string) (workspace.Diff, error) {
	if s == nil || s.backend == nil {
		return workspace.Diff{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.Diff(ctx, baseID)
}

func (s *workspaceStore) Checkpoint(ctx context.Context, opts workspace.CheckpointOptions) (workspace.Checkpoint, error) {
	if s == nil || s.backend == nil {
		return workspace.Checkpoint{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.Checkpoint(ctx, opts)
}

func (s *workspaceStore) Restore(ctx context.Context, id string) (workspace.Checkpoint, error) {
	if s == nil || s.backend == nil {
		return workspace.Checkpoint{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.Restore(ctx, id)
}

func (s *workspaceStore) ListCheckpoints(ctx context.Context) ([]workspace.Checkpoint, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.backend.ListCheckpoints(ctx)
}

type workspaceStoreWithPreview struct {
	*workspaceStore
	previewer WorkspacePreviewer
}

func (s workspaceStoreWithPreview) PreviewPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	if s.workspaceStore == nil || s.workspaceStore.backend == nil || s.previewer == nil {
		return workspace.PatchResult{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.previewer.PreviewPatch(ctx, ops)
}

type workspaceStoreWithUnifiedDiff struct {
	*workspaceStore
	patcher WorkspaceUnifiedDiffPatcher
}

func (s workspaceStoreWithUnifiedDiff) ApplyUnifiedDiff(ctx context.Context, diff string, opts workspace.PatchOptions) (workspace.PatchResult, error) {
	if s.workspaceStore == nil || s.workspaceStore.backend == nil || s.patcher == nil {
		return workspace.PatchResult{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.patcher.ApplyUnifiedDiff(ctx, diff, opts)
}

type workspaceStoreWithPreviewAndUnifiedDiff struct {
	*workspaceStore
	previewer WorkspacePreviewer
	patcher   WorkspaceUnifiedDiffPatcher
}

func (s workspaceStoreWithPreviewAndUnifiedDiff) PreviewPatch(ctx context.Context, ops []workspace.PatchOperation) (workspace.PatchResult, error) {
	if s.workspaceStore == nil || s.workspaceStore.backend == nil || s.previewer == nil {
		return workspace.PatchResult{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.previewer.PreviewPatch(ctx, ops)
}

func (s workspaceStoreWithPreviewAndUnifiedDiff) ApplyUnifiedDiff(ctx context.Context, diff string, opts workspace.PatchOptions) (workspace.PatchResult, error) {
	if s.workspaceStore == nil || s.workspaceStore.backend == nil || s.patcher == nil {
		return workspace.PatchResult{}, fmt.Errorf("sandbox: workspace backend is required")
	}
	return s.patcher.ApplyUnifiedDiff(ctx, diff, opts)
}

// NewCommandRunner adapts a sandbox command backend to commandtools.Runner.
func NewCommandRunner(backend CommandRunner) commandtools.Runner {
	return commandRunnerAdapter{backend: backend}
}

type commandRunnerAdapter struct {
	backend CommandRunner
}

func (a commandRunnerAdapter) RunCommand(ctx context.Context, req commandtools.Request) (commandtools.Result, error) {
	if a.backend == nil {
		return commandtools.Result{}, fmt.Errorf("sandbox: command backend is required")
	}
	return a.backend.RunCommand(ctx, req)
}

// NewSessionTools returns the standard managed command-session tool set over a
// sandbox session manager. It mirrors commandtools.NewSessionTools and returns
// an error only when backend is nil. This eager check is intentional: unlike
// the thin passthrough adapters, the tool factory constructs concrete tools up
// front and should fail before exposing a partially wired managed-session
// surface.
func NewSessionTools(backend SessionManager) ([]tool.Tool, error) {
	if backend == nil {
		return nil, fmt.Errorf("sandbox: session backend is required")
	}
	tools := []tool.Tool{
		commandtools.NewStartTool(sessionStarterAdapter{backend: backend}),
		commandtools.NewReadOutputTool(sessionReaderAdapter{backend: backend}),
		commandtools.NewStopTool(sessionStopperAdapter{backend: backend}),
		commandtools.NewListTool(sessionListerAdapter{backend: backend}),
	}
	if writer, ok := any(backend).(SessionWriter); ok {
		tools = append(tools, commandtools.NewWriteInputTool(sessionWriterAdapter{backend: writer}))
	}
	if resizer, ok := any(backend).(SessionResizer); ok {
		tools = append(tools, commandtools.NewResizeTool(sessionResizerAdapter{backend: resizer}))
	}
	return tools, nil
}

// SessionCleanupOptions returns hook options that clean up sandbox-managed
// command sessions when an agent session ends.
func SessionCleanupOptions(cleaner SessionCleaner) []hook.Option {
	if cleaner == nil {
		return nil
	}
	return commandtools.SessionCleanupOptions(NewSessionCleaner(cleaner))
}

// NewSessionCleaner adapts a sandbox cleanup backend to commandtools.Cleaner.
func NewSessionCleaner(cleaner SessionCleaner) commandtools.Cleaner {
	return sessionCleanerAdapter{backend: cleaner}
}

type sessionStarterAdapter struct {
	backend SessionManager
}

func (a sessionStarterAdapter) StartCommand(ctx context.Context, req commandtools.StartRequest) (commandtools.CommandSession, error) {
	if a.backend == nil {
		return commandtools.CommandSession{}, fmt.Errorf("sandbox: session backend is required")
	}
	return a.backend.StartCommand(ctx, req)
}

type sessionReaderAdapter struct {
	backend SessionManager
}

func (a sessionReaderAdapter) ReadCommandOutput(ctx context.Context, req commandtools.ReadRequest) (commandtools.ReadResult, error) {
	if a.backend == nil {
		return commandtools.ReadResult{}, fmt.Errorf("sandbox: session backend is required")
	}
	return a.backend.ReadCommandOutput(ctx, req)
}

type sessionStopperAdapter struct {
	backend SessionManager
}

func (a sessionStopperAdapter) StopCommand(ctx context.Context, req commandtools.StopRequest) (commandtools.CommandSession, error) {
	if a.backend == nil {
		return commandtools.CommandSession{}, fmt.Errorf("sandbox: session backend is required")
	}
	return a.backend.StopCommand(ctx, req)
}

type sessionListerAdapter struct {
	backend SessionManager
}

func (a sessionListerAdapter) ListCommands(ctx context.Context, req commandtools.ListRequest) ([]commandtools.CommandSession, error) {
	if a.backend == nil {
		return nil, fmt.Errorf("sandbox: session backend is required")
	}
	return a.backend.ListCommands(ctx, req)
}

type sessionWriterAdapter struct {
	backend SessionWriter
}

func (a sessionWriterAdapter) WriteCommandInput(ctx context.Context, req commandtools.WriteRequest) (commandtools.WriteResult, error) {
	if a.backend == nil {
		return commandtools.WriteResult{}, fmt.Errorf("sandbox: session writer backend is required")
	}
	return a.backend.WriteCommandInput(ctx, req)
}

type sessionResizerAdapter struct {
	backend SessionResizer
}

func (a sessionResizerAdapter) ResizeCommandTerminal(ctx context.Context, req commandtools.ResizeRequest) (commandtools.CommandSession, error) {
	if a.backend == nil {
		return commandtools.CommandSession{}, fmt.Errorf("sandbox: session resizer backend is required")
	}
	return a.backend.ResizeCommandTerminal(ctx, req)
}

type sessionCleanerAdapter struct {
	backend SessionCleaner
}

func (a sessionCleanerAdapter) CleanupSession(ctx context.Context, sessionID string) error {
	if a.backend == nil {
		return fmt.Errorf("sandbox: session cleaner backend is required")
	}
	return a.backend.CleanupSession(ctx, sessionID)
}

var (
	_ Workspace                   = workspace.Store(nil)
	_ workspace.Store             = (*workspaceStore)(nil)
	_ commandtools.Runner         = commandRunnerAdapter{}
	_ commandtools.Cleaner        = sessionCleanerAdapter{}
	_ WorkspacePreviewer          = workspaceStoreWithPreview{}
	_ WorkspacePreviewer          = workspaceStoreWithPreviewAndUnifiedDiff{}
	_ WorkspaceUnifiedDiffPatcher = workspaceStoreWithUnifiedDiff{}
	_ WorkspaceUnifiedDiffPatcher = workspaceStoreWithPreviewAndUnifiedDiff{}
)
