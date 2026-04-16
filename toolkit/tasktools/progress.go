package tasktools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
)

// ProgressStore is the minimal task store capability needed to update task
// progress from tool outcomes.
type ProgressStore interface {
	List(context.Context) ([]Task, error)
	Upsert(context.Context, Task) (Task, error)
}

// VerificationProgressOption configures NewVerificationProgressVerifier.
type VerificationProgressOption func(*verificationProgressConfig)

// WithVerificationPassStatus sets the task status used when verification
// passes. The default is completed. An empty status disables pass updates.
func WithVerificationPassStatus(status Status) VerificationProgressOption {
	return func(c *verificationProgressConfig) {
		c.passStatus = status
	}
}

// WithVerificationFailStatus sets the task status used when verification
// fails. The default is in_progress. An empty status disables failure updates.
func WithVerificationFailStatus(status Status) VerificationProgressOption {
	return func(c *verificationProgressConfig) {
		c.failStatus = status
	}
}

// NewVerificationProgressVerifier wraps verifier so verification results can
// update task progress when verifytools.Request.Metadata includes
// model.MetadataTaskID. The update is host-owned and explicit: callers opt into
// this wrapper, and actual verification still runs through the configured
// verifytools.Verifier.
//
// Task update failures, including missing task IDs and task store errors, are
// recorded in the returned result metadata under
// model.MetadataTaskProgressError rather than failing verification itself. This
// keeps verification diagnostics recoverable by the model while making progress
// persistence problems observable to hosts.
func NewVerificationProgressVerifier(store ProgressStore, verifier verifytools.Verifier, opts ...VerificationProgressOption) verifytools.Verifier {
	config := verificationProgressConfig{
		store:      store,
		verifier:   verifier,
		passStatus: StatusCompleted,
		failStatus: StatusInProgress,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	return verificationProgressVerifier{config: config}
}

type verificationProgressConfig struct {
	store      ProgressStore
	verifier   verifytools.Verifier
	passStatus Status
	failStatus Status
}

type verificationProgressVerifier struct {
	config verificationProgressConfig
}

func (v verificationProgressVerifier) Verify(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
	if v.config.verifier == nil {
		return verifytools.Result{}, fmt.Errorf("tasktools: verifier is required")
	}
	result, err := v.config.verifier.Verify(ctx, req)
	if err != nil {
		return result, err
	}
	taskID := metadataString(req.Metadata, model.MetadataTaskID)
	if taskID == "" || v.config.store == nil {
		return result, nil
	}
	status := v.config.failStatus
	if result.Passed {
		status = v.config.passStatus
	}
	if status == "" {
		return result, nil
	}
	if !isValidStatus(status) {
		setProgressError(&result, fmt.Errorf("invalid progress status: %s", status))
		return result, nil
	}
	if err := updateTaskFromVerification(ctx, v.config.store, taskID, status, req, result); err != nil {
		setProgressError(&result, err)
		return result, nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata[model.MetadataTaskID] = taskID
	result.Metadata[model.MetadataTaskStatus] = string(status)
	result.Metadata[model.MetadataTaskEvidence] = verificationEvidence(req, result)
	return result, nil
}

func updateTaskFromVerification(ctx context.Context, store ProgressStore, taskID string, status Status, req verifytools.Request, result verifytools.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task, ok, err := findTask(ctx, store, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.Status = status
	task.Notes = appendTaskNote(task.Notes, verificationNote(req, result))
	task.Evidence = mergeStrings(task.Evidence, verificationEvidence(req, result))
	_, err = store.Upsert(ctx, task)
	if err != nil {
		return fmt.Errorf("update task progress: %w", err)
	}
	return nil
}

func findTask(ctx context.Context, store ProgressStore, taskID string) (Task, bool, error) {
	tasks, err := store.List(ctx)
	if err != nil {
		return Task{}, false, fmt.Errorf("list tasks: %w", err)
	}
	for _, task := range tasks {
		if task.ID == taskID {
			return task, true, nil
		}
	}
	return Task{}, false, nil
}

func verificationNote(req verifytools.Request, result verifytools.Result) string {
	status := "failed"
	if result.Passed {
		status = "passed"
	}
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = strings.TrimSpace(req.Name)
	}
	if name == "" {
		name = "verification"
	}
	note := fmt.Sprintf("verification %s %s", name, status)
	if diagnostics := len(result.Diagnostics); diagnostics > 0 {
		note = fmt.Sprintf("%s with %d diagnostics", note, diagnostics)
	}
	return note
}

func verificationEvidence(req verifytools.Request, result verifytools.Result) []string {
	var values []string
	if name := strings.TrimSpace(result.Name); name != "" {
		values = append(values, "verification:"+name)
	} else if name := strings.TrimSpace(req.Name); name != "" {
		values = append(values, "verification:"+name)
	}
	if target := strings.TrimSpace(req.Target); target != "" {
		values = append(values, target)
	}
	for _, diagnostic := range result.Diagnostics {
		if path := strings.TrimSpace(diagnostic.Path); path != "" {
			values = append(values, path)
		}
	}
	return mergeStrings(nil, values)
}

func appendTaskNote(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if note == "" || strings.Contains(existing, note) {
		return existing
	}
	if existing == "" {
		return note
	}
	return existing + "; " + note
}

func mergeStrings(existing []string, values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(values))
	for _, value := range existing {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}

func setProgressError(result *verifytools.Result, err error) {
	if err == nil {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata[model.MetadataTaskProgressError] = err.Error()
}
