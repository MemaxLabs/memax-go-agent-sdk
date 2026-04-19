package cloudmanaged

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// AuditConfig configures optional cloud-managed event auditing.
type AuditConfig struct {
	Sink         AuditSink
	ErrorHandler AuditErrorHandler
}

// AuditSink persists one structured audit record.
type AuditSink interface {
	WriteAudit(context.Context, AuditRecord) error
}

// AuditSinkFunc adapts a function to AuditSink.
type AuditSinkFunc func(context.Context, AuditRecord) error

// WriteAudit implements AuditSink.
func (f AuditSinkFunc) WriteAudit(ctx context.Context, record AuditRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}

// AuditErrorHandler observes non-fatal audit write failures.
type AuditErrorHandler interface {
	HandleAuditError(context.Context, error)
}

// AuditErrorHandlerFunc adapts a function to AuditErrorHandler.
type AuditErrorHandlerFunc func(context.Context, error)

// HandleAuditError implements AuditErrorHandler.
func (f AuditErrorHandlerFunc) HandleAuditError(ctx context.Context, err error) {
	if f != nil {
		f(ctx, err)
	}
}

// Subscriber mirrors an event stream to a host-owned audit sink while
// preserving the original event order for the caller.
type Subscriber interface {
	Subscribe(context.Context, <-chan memaxagent.Event) <-chan memaxagent.Event
}

// AuditRecord is the structured managed-runtime record written to audit sinks.
// It mirrors the event stream with JSON-friendly payloads and stringified
// errors so hosts can persist one stable object per event without parsing
// transcript text.
type AuditRecord struct {
	Kind            memaxagent.EventKind              `json:"kind"`
	SessionID       string                            `json:"session_id,omitempty"`
	ParentSessionID string                            `json:"parent_session_id,omitempty"`
	Turn            int                               `json:"turn,omitempty"`
	Time            time.Time                         `json:"time"`
	Message         *model.Message                    `json:"message,omitempty"`
	ToolUse         *model.ToolUse                    `json:"tool_use,omitempty"`
	ToolUseDelta    string                            `json:"tool_use_delta,omitempty"`
	ToolResult      *model.ToolResult                 `json:"tool_result,omitempty"`
	Usage           *model.Usage                      `json:"usage,omitempty"`
	Context         *memaxagent.ContextEvent          `json:"context,omitempty"`
	Compaction      *AuditCompactionRecord            `json:"compaction,omitempty"`
	Memory          *memaxagent.MemoryCandidatesEvent `json:"memory,omitempty"`
	Skill           *memaxagent.SkillEvent            `json:"skill,omitempty"`
	Workspace       *memaxagent.WorkspaceEvent        `json:"workspace,omitempty"`
	Verification    *memaxagent.VerificationEvent     `json:"verification,omitempty"`
	Approval        *memaxagent.ApprovalEvent         `json:"approval,omitempty"`
	Tenant          *memaxagent.TenantEvent           `json:"tenant,omitempty"`
	Command         *memaxagent.CommandEvent          `json:"command,omitempty"`
	Run             *memaxagent.RunEvent              `json:"run,omitempty"`
	Result          string                            `json:"result,omitempty"`
	Error           string                            `json:"error,omitempty"`
}

// AuditCompactionRecord is the JSON-friendly audit shape for context
// compaction provenance.
type AuditCompactionRecord struct {
	Policy             string `json:"policy,omitempty"`
	Reason             string `json:"reason,omitempty"`
	OriginalMessages   int    `json:"original_messages,omitempty"`
	SentMessages       int    `json:"sent_messages,omitempty"`
	SummarizedMessages int    `json:"summarized_messages,omitempty"`
	RetainedMessages   int    `json:"retained_messages,omitempty"`
	ReplacedSummaries  int    `json:"replaced_summaries,omitempty"`
	SummaryHash        string `json:"summary_hash,omitempty"`
	SummaryPreview     string `json:"summary_preview,omitempty"`
}

// JSONLSink writes one JSON object per line to w.
type JSONLSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLSink constructs a JSONL audit sink.
func NewJSONLSink(w io.Writer) *JSONLSink {
	return &JSONLSink{w: w}
}

// WriteAudit implements AuditSink.
func (s *JSONLSink) WriteAudit(_ context.Context, record AuditRecord) error {
	if s == nil || s.w == nil {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// MemorySink stores audit records in memory for tests and local inspection.
type MemorySink struct {
	mu      sync.Mutex
	records []AuditRecord
}

// WriteAudit implements AuditSink.
func (s *MemorySink) WriteAudit(_ context.Context, record AuditRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneAuditRecord(record))
	return nil
}

// Records returns a defensive copy of the stored records.
func (s *MemorySink) Records() []AuditRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditRecord, len(s.records))
	for i, record := range s.records {
		out[i] = cloneAuditRecord(record)
	}
	return out
}

type auditSubscriber struct {
	config AuditConfig
}

// ObserveEvent implements memaxagent.EventObserver.
func (c AuditConfig) ObserveEvent(ctx context.Context, event memaxagent.Event) {
	if c.Sink == nil {
		return
	}
	if err := c.Sink.WriteAudit(ctx, recordFromEvent(event)); err != nil {
		c.handleError(ctx, err)
	}
}

// Subscribe mirrors events to the configured sink while preserving the event
// stream for the caller.
func (c AuditConfig) Subscribe(ctx context.Context, events <-chan memaxagent.Event) <-chan memaxagent.Event {
	return auditSubscriber{config: c}.Subscribe(ctx, events)
}

// Subscribe implements Subscriber.
func (s auditSubscriber) Subscribe(ctx context.Context, events <-chan memaxagent.Event) <-chan memaxagent.Event {
	if s.config.Sink == nil || events == nil {
		return events
	}
	out := make(chan memaxagent.Event)
	go func() {
		defer close(out)
		for event := range events {
			s.config.ObserveEvent(ctx, event)
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out
}

func (c AuditConfig) subscribe(ctx context.Context, events <-chan memaxagent.Event) <-chan memaxagent.Event {
	return c.Subscribe(ctx, events)
}

func (c AuditConfig) handleError(ctx context.Context, err error) {
	if err == nil || c.ErrorHandler == nil {
		return
	}
	c.ErrorHandler.HandleAuditError(ctx, err)
}

func recordFromEvent(event memaxagent.Event) AuditRecord {
	record := AuditRecord{
		Kind:            event.Kind,
		SessionID:       event.SessionID,
		ParentSessionID: event.ParentSessionID,
		Turn:            event.Turn,
		Time:            event.Time,
		ToolUseDelta:    event.ToolUseDelta,
		Result:          event.Result,
	}
	if record.Time.IsZero() {
		record.Time = time.Now().UTC()
	}
	if event.Message != nil {
		msg := model.CloneMessage(*event.Message)
		record.Message = &msg
	}
	if event.ToolUse != nil {
		use := *event.ToolUse
		use.Input = append([]byte(nil), event.ToolUse.Input...)
		record.ToolUse = &use
	}
	if event.ToolResult != nil {
		result := *event.ToolResult
		result.Metadata = model.CloneMetadata(result.Metadata)
		record.ToolResult = &result
	}
	if event.Usage != nil {
		usage := *event.Usage
		usage.Metadata = model.CloneMetadata(usage.Metadata)
		record.Usage = &usage
	}
	if event.Context != nil {
		contextEvent := *event.Context
		record.Context = &contextEvent
	}
	if event.Compaction != nil {
		record.Compaction = &AuditCompactionRecord{
			Policy:             event.Compaction.Policy,
			Reason:             string(event.Compaction.Reason),
			OriginalMessages:   event.Compaction.OriginalMessages,
			SentMessages:       event.Compaction.SentMessages,
			SummarizedMessages: event.Compaction.SummarizedMessages,
			RetainedMessages:   event.Compaction.RetainedMessages,
			ReplacedSummaries:  event.Compaction.ReplacedSummaries,
			SummaryHash:        event.Compaction.SummaryHash,
			SummaryPreview:     event.Compaction.SummaryPreview,
		}
	}
	if event.Memory != nil {
		memoryEvent := *event.Memory
		memoryEvent.Candidates = memory.CloneCandidates(event.Memory.Candidates)
		record.Memory = &memoryEvent
	}
	if event.Skill != nil {
		skillEvent := *event.Skill
		skillEvent.SelectedSkills = append([]string(nil), skillEvent.SelectedSkills...)
		record.Skill = &skillEvent
	}
	if event.Workspace != nil {
		workspaceEvent := *event.Workspace
		workspaceEvent.Paths = append([]string(nil), workspaceEvent.Paths...)
		record.Workspace = &workspaceEvent
	}
	if event.Verification != nil {
		verificationEvent := *event.Verification
		verificationEvent.Paths = append([]string(nil), verificationEvent.Paths...)
		record.Verification = &verificationEvent
	}
	if event.Approval != nil {
		approvalEvent := *event.Approval
		approvalEvent.Summary.Paths = append([]string(nil), approvalEvent.Summary.Paths...)
		record.Approval = &approvalEvent
	}
	if event.Tenant != nil {
		tenantEvent := *event.Tenant
		tenantEvent.Attributes = cloneStringMap(tenantEvent.Attributes)
		record.Tenant = &tenantEvent
	}
	if event.Command != nil {
		commandEvent := *event.Command
		commandEvent.Argv = append([]string(nil), commandEvent.Argv...)
		record.Command = &commandEvent
	}
	if event.Run != nil {
		runEvent := *event.Run
		record.Run = &runEvent
	}
	if event.Err != nil {
		record.Error = event.Err.Error()
	}
	return record
}

func cloneAuditRecord(record AuditRecord) AuditRecord {
	clone := record
	if record.Message != nil {
		msg := model.CloneMessage(*record.Message)
		clone.Message = &msg
	}
	if record.ToolUse != nil {
		use := *record.ToolUse
		use.Input = append([]byte(nil), record.ToolUse.Input...)
		clone.ToolUse = &use
	}
	if record.ToolResult != nil {
		result := *record.ToolResult
		result.Metadata = model.CloneMetadata(result.Metadata)
		clone.ToolResult = &result
	}
	if record.Usage != nil {
		usage := *record.Usage
		usage.Metadata = model.CloneMetadata(usage.Metadata)
		clone.Usage = &usage
	}
	if record.Context != nil {
		contextEvent := *record.Context
		clone.Context = &contextEvent
	}
	if record.Compaction != nil {
		compaction := *record.Compaction
		clone.Compaction = &compaction
	}
	if record.Memory != nil {
		memoryEvent := *record.Memory
		memoryEvent.Candidates = memory.CloneCandidates(record.Memory.Candidates)
		clone.Memory = &memoryEvent
	}
	if record.Skill != nil {
		skillEvent := *record.Skill
		skillEvent.SelectedSkills = append([]string(nil), skillEvent.SelectedSkills...)
		clone.Skill = &skillEvent
	}
	if record.Workspace != nil {
		workspaceEvent := *record.Workspace
		workspaceEvent.Paths = append([]string(nil), workspaceEvent.Paths...)
		clone.Workspace = &workspaceEvent
	}
	if record.Verification != nil {
		verificationEvent := *record.Verification
		verificationEvent.Paths = append([]string(nil), verificationEvent.Paths...)
		clone.Verification = &verificationEvent
	}
	if record.Approval != nil {
		approvalEvent := *record.Approval
		approvalEvent.Summary.Paths = append([]string(nil), approvalEvent.Summary.Paths...)
		clone.Approval = &approvalEvent
	}
	if record.Tenant != nil {
		tenantEvent := *record.Tenant
		tenantEvent.Attributes = cloneStringMap(tenantEvent.Attributes)
		clone.Tenant = &tenantEvent
	}
	if record.Command != nil {
		commandEvent := *record.Command
		commandEvent.Argv = append([]string(nil), commandEvent.Argv...)
		clone.Command = &commandEvent
	}
	if record.Run != nil {
		runEvent := *record.Run
		clone.Run = &runEvent
	}
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
