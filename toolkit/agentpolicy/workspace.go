// Package agentpolicy provides composable host-owned policy presets for common
// agent workflows. Policies are implemented as hooks and helpers rather than
// hidden core behavior, so applications can combine them with their own
// permission, review, and telemetry layers.
package agentpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
)

const checkpointBeforePatchReason = "create a workspace checkpoint before applying patches"
const verifyBeforeFinalReason = "run workspace verification successfully after the latest workspace change before finalizing"
const verifyBeforeFinalAfterCommandReason = "run workspace verification successfully after the latest command before finalizing"

const (
	// MetadataRollbackRecommended marks verification results where rollback
	// guidance was attached by RollbackOnFailedVerification.
	MetadataRollbackRecommended = "rollback_recommended"
	// MetadataRollbackCheckpointID carries the checkpoint ID recommended for
	// rollback after failed verification.
	MetadataRollbackCheckpointID = "rollback_checkpoint_id"
)

// CheckpointBeforePatch denies mutating workspace patches until the current
// session has successfully created a workspace checkpoint. Dry-run patch
// previews are allowed because they do not mutate workspace state.
type CheckpointBeforePatch struct {
	mu           sync.RWMutex
	checkpointed map[string]bool
}

// RequireCheckpointBeforePatch returns a policy that can be installed into a
// hook runner with hook.NewRunner(policy.Options()...).
func RequireCheckpointBeforePatch() *CheckpointBeforePatch {
	return &CheckpointBeforePatch{checkpointed: map[string]bool{}}
}

// Options returns hook options for this policy.
func (p *CheckpointBeforePatch) Options() []hook.Option {
	return []hook.Option{
		hook.WithBeforeToolUse(p.BeforeToolUse),
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// BeforeToolUse denies workspace patches until a checkpoint has been observed
// in the same session.
func (p *CheckpointBeforePatch) BeforeToolUse(ctx context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeToolUseResult{}, err
	}
	if input.Use.Name != workspacetools.ApplyPatchToolName {
		return hook.BeforeToolUseResult{}, nil
	}
	if patchDryRun(input.Use.Input) {
		return hook.BeforeToolUseResult{}, nil
	}
	if p.hasCheckpoint(input.SessionID) {
		return hook.BeforeToolUseResult{}, nil
	}
	return hook.BeforeToolUseResult{DenyReason: checkpointBeforePatchReason}, nil
}

// AfterToolUse records successful checkpoint creation.
func (p *CheckpointBeforePatch) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.Result.IsError || input.Use.Name != workspacetools.CheckpointToolName {
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataWorkspaceOperation].(string); operation != "checkpoint" {
		return nil
	}
	if input.SessionID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkpointed == nil {
		p.checkpointed = map[string]bool{}
	}
	p.checkpointed[input.SessionID] = true
	return nil
}

// SessionEnded removes checkpoint state for the completed session.
func (p *CheckpointBeforePatch) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes checkpoint state for sessionID. It is safe to call on a nil
// policy or with an empty session ID.
func (p *CheckpointBeforePatch) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.checkpointed, sessionID)
}

func (p *CheckpointBeforePatch) hasCheckpoint(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.checkpointed[sessionID]
}

func patchDryRun(input json.RawMessage) bool {
	var value struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(input, &value); err != nil {
		return false
	}
	return value.DryRun
}

// CheckpointBeforePatchReason returns the model-visible denial reason used by
// RequireCheckpointBeforePatch. It is exported for tests and host UI copy.
func CheckpointBeforePatchReason() string {
	return checkpointBeforePatchReason
}

// RollbackOnFailedVerification records the latest workspace checkpoint for each
// session and wraps a verifier so failed checks include model-visible rollback
// guidance. The policy does not restore automatically; the model must call the
// normal workspace restore tool, keeping rollback explicit and observable.
type RollbackOnFailedVerification struct {
	mu          sync.RWMutex
	checkpoints map[string]string
}

// ApprovalBeforeTool denies configured tools until the model obtains approval
// for the tool name through approvaltools.ToolName in the same session. By
// default, approvals are reusable for that session and action. Use
// WithSingleUseApprovals or WithInputBoundApprovals when a host needs stricter
// per-attempt or exact-input approval semantics.
type ApprovalBeforeTool struct {
	mu               sync.RWMutex
	required         map[string]struct{}
	approved         map[string]map[string][]approvalGrant
	approvalToolName string
	singleUse        bool
	bindInput        bool
}

type approvalGrant struct {
	InputHash string
}

// ApprovalBeforeToolOption configures RequireApprovalBeforeTools.
type ApprovalBeforeToolOption func(*ApprovalBeforeTool)

// VerifyBeforeFinal tracks workspace changes and denies final answers until a
// successful verification result has been observed after the latest mutation.
// It is recommendation-free and execution-free: the model must call the
// configured verification tool itself through the normal tool pipeline.
type VerifyBeforeFinal struct {
	mu    sync.RWMutex
	dirty map[string]bool
}

// CommandMatcher matches simple command token prefixes for command policy
// presets. Shell-string command inputs with shell control syntax are not
// prefix-matchable and are denied by command allow/deny policies; exact argv
// tools and command metadata are matched by argv element.
type CommandMatcher struct {
	Prefix []string
}

// MatchCommandPrefix returns a matcher that matches commands whose token prefix
// starts with prefix.
func MatchCommandPrefix(prefix ...string) CommandMatcher {
	return CommandMatcher{Prefix: normalizeCommandArgs(prefix)}
}

// CommandPolicy denies run_command calls according to an allowlist or denylist.
type CommandPolicy struct {
	toolName string
	allow    []CommandMatcher
	deny     []CommandMatcher
}

// CommandPolicyOption configures command policy presets.
type CommandPolicyOption func(*CommandPolicy)

// WithCommandToolName configures the command tool name watched by command
// policies. It defaults to commandtools.ToolName.
func WithCommandToolName(name string) CommandPolicyOption {
	return func(p *CommandPolicy) {
		if name = strings.TrimSpace(name); name != "" {
			p.toolName = name
		}
	}
}

// AllowCommands returns a before-tool policy that allows only matching
// run_command prefixes. Non-command tools are ignored.
func AllowCommands(matchers ...CommandMatcher) *CommandPolicy {
	return AllowCommandsWithOptions(matchers, nil)
}

// AllowCommandsWithOptions returns a before-tool policy that allows only
// matching command prefixes, applying optional command policy settings.
func AllowCommandsWithOptions(matchers []CommandMatcher, options ...CommandPolicyOption) *CommandPolicy {
	p := &CommandPolicy{toolName: commandtools.ToolName, allow: cloneCommandMatchers(matchers)}
	for _, option := range options {
		if option != nil {
			option(p)
		}
	}
	return p
}

// DenyCommands returns a before-tool policy that denies matching run_command
// prefixes. Non-command tools are ignored.
func DenyCommands(matchers ...CommandMatcher) *CommandPolicy {
	return DenyCommandsWithOptions(matchers, nil)
}

// DenyCommandsWithOptions returns a before-tool policy that denies matching
// command prefixes, applying optional command policy settings.
func DenyCommandsWithOptions(matchers []CommandMatcher, options ...CommandPolicyOption) *CommandPolicy {
	p := &CommandPolicy{toolName: commandtools.ToolName, deny: cloneCommandMatchers(matchers)}
	for _, option := range options {
		if option != nil {
			option(p)
		}
	}
	return p
}

// CommandApprovalPolicyOption configures command approval policy presets.
type CommandApprovalPolicyOption func(*CommandApprovalPolicy)

// WithCommandSingleUseApprovals makes each command approval grant permit one
// matching command attempt. The grant is consumed when BeforeToolUse allows the
// command.
func WithCommandSingleUseApprovals() CommandApprovalPolicyOption {
	return func(p *CommandApprovalPolicy) {
		p.singleUse = true
	}
}

// WithCommandInputBoundApprovals requires request_approval results to include
// approvaltools.MetadataApprovalInputHash and only allows a later command call
// whose input has the same canonical JSON hash. The request_approval tool
// produces that hash when the model includes tool_input in its request.
func WithCommandInputBoundApprovals() CommandApprovalPolicyOption {
	return func(p *CommandApprovalPolicy) {
		p.bindInput = true
	}
}

// WithCommandApprovalToolName configures the approval tool name watched by this
// command approval policy. It defaults to approvaltools.ToolName.
func WithCommandApprovalToolName(name string) CommandApprovalPolicyOption {
	return func(p *CommandApprovalPolicy) {
		if name = strings.TrimSpace(name); name != "" {
			p.approvalToolName = name
		}
	}
}

// WithCommandApprovalCommandToolName configures the command tool name gated by
// this command approval policy. It defaults to commandtools.ToolName.
func WithCommandApprovalCommandToolName(name string) CommandApprovalPolicyOption {
	return func(p *CommandApprovalPolicy) {
		if name = strings.TrimSpace(name); name != "" {
			p.commandToolName = name
		}
	}
}

// RequireApprovalBeforeCommands denies matching run_command calls until the
// model obtains approval for commandtools.ToolName through request_approval.
// Without WithCommandInputBoundApprovals, a granted approval authorizes later
// matching command prefixes for the session. WithCommandInputBoundApprovals
// binds approval to the exact later command input.
func RequireApprovalBeforeCommands(matchers []CommandMatcher, options ...CommandApprovalPolicyOption) *CommandApprovalPolicy {
	p := &CommandApprovalPolicy{
		matchers:         cloneCommandMatchers(matchers),
		approved:         map[string][]approvalGrant{},
		commandToolName:  commandtools.ToolName,
		approvalToolName: approvaltools.ToolName,
	}
	for _, option := range options {
		if option != nil {
			option(p)
		}
	}
	return p
}

// CommandApprovalPolicy gates selected commands behind approval results.
type CommandApprovalPolicy struct {
	mu               sync.RWMutex
	matchers         []CommandMatcher
	approved         map[string][]approvalGrant
	commandToolName  string
	approvalToolName string
	singleUse        bool
	bindInput        bool
}

// VerifyAfterCommands tracks successful matching commands and denies final
// answers until successful workspace verification occurs in the same session.
type VerifyAfterCommands struct {
	mu              sync.RWMutex
	dirty           map[string]bool
	matchers        []CommandMatcher
	commandToolName string
}

// WithSingleUseApprovals makes each approval grant permit one matching tool
// attempt. The grant is consumed when BeforeToolUse allows the attempt.
func WithSingleUseApprovals() ApprovalBeforeToolOption {
	return func(p *ApprovalBeforeTool) {
		p.singleUse = true
	}
}

// WithInputBoundApprovals requires request_approval results to include
// approvaltools.MetadataApprovalInputHash and only allows a later tool call
// whose input has the same canonical JSON hash. The request_approval tool
// produces that hash when the model includes tool_input in its request.
func WithInputBoundApprovals() ApprovalBeforeToolOption {
	return func(p *ApprovalBeforeTool) {
		p.bindInput = true
	}
}

// WithApprovalToolName configures the approval tool name watched by this
// policy. It defaults to approvaltools.ToolName.
func WithApprovalToolName(name string) ApprovalBeforeToolOption {
	return func(p *ApprovalBeforeTool) {
		if name = strings.TrimSpace(name); name != "" {
			p.approvalToolName = name
		}
	}
}

// RequireApprovalBeforeTools returns a policy that gates the given tool names
// behind explicit request_approval results. Empty tool names are ignored.
//
// The Approver behind request_approval is the security boundary. Static
// always-approve implementations are useful for tests and trusted automation,
// but production hosts should connect the tool to their real approval workflow.
func RequireApprovalBeforeTools(toolNames ...string) *ApprovalBeforeTool {
	return RequireApprovalBeforeToolsWithOptions(toolNames, nil)
}

// RequireApprovalBeforeToolsWithOptions returns a policy that gates the given
// tool names behind approval results and applies optional stricter grant
// semantics. Passing nil options preserves the default reusable session grant
// behavior.
func RequireApprovalBeforeToolsWithOptions(toolNames []string, options ...ApprovalBeforeToolOption) *ApprovalBeforeTool {
	p := &ApprovalBeforeTool{
		required:         map[string]struct{}{},
		approved:         map[string]map[string][]approvalGrant{},
		approvalToolName: approvaltools.ToolName,
	}
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name != "" {
			p.required[name] = struct{}{}
		}
	}
	for _, option := range options {
		if option != nil {
			option(p)
		}
	}
	return p
}

// Options returns hook options for approval-before-tool gating.
func (p *ApprovalBeforeTool) Options() []hook.Option {
	return []hook.Option{
		hook.WithBeforeToolUse(p.BeforeToolUse),
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// BeforeToolUse denies configured tools until approval has been observed for
// the same session and tool name.
func (p *ApprovalBeforeTool) BeforeToolUse(ctx context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeToolUseResult{}, err
	}
	if !p.requires(input.Use.Name) {
		return hook.BeforeToolUseResult{}, nil
	}
	if consumed, metadata := p.consumeApproval(input.SessionID, input.Use); consumed {
		return hook.BeforeToolUseResult{Metadata: metadata}, nil
	}
	return hook.BeforeToolUseResult{DenyReason: ApprovalBeforeToolReason(input.Use.Name)}, nil
}

// AfterToolUse records granted approvals from approvaltools tool results.
func (p *ApprovalBeforeTool) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.SessionID == "" || input.Result.IsError || input.Use.Name != p.approvalName() {
		return nil
	}
	if operation, _ := input.Result.Metadata[approvaltools.MetadataApprovalOperation].(string); operation != "request" {
		return nil
	}
	approved, _ := input.Result.Metadata[approvaltools.MetadataApprovalApproved].(bool)
	action, _ := input.Result.Metadata[approvaltools.MetadataApprovalAction].(string)
	action = strings.TrimSpace(action)
	if !approved || !p.requires(action) {
		return nil
	}
	inputHash, _ := input.Result.Metadata[approvaltools.MetadataApprovalInputHash].(string)
	inputHash = strings.TrimSpace(inputHash)
	if p.bindInput && inputHash == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approved == nil {
		p.approved = map[string]map[string][]approvalGrant{}
	}
	if p.approved[input.SessionID] == nil {
		p.approved[input.SessionID] = map[string][]approvalGrant{}
	}
	p.approved[input.SessionID][action] = append(p.approved[input.SessionID][action], approvalGrant{InputHash: inputHash})
	return nil
}

// SessionEnded removes approval state for the completed session.
func (p *ApprovalBeforeTool) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes approval state for sessionID. It is safe to call on a nil
// policy or with an empty session ID.
func (p *ApprovalBeforeTool) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.approved, sessionID)
}

func (p *ApprovalBeforeTool) requires(toolName string) bool {
	if p == nil || toolName == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.required[toolName]
	return ok
}

func (p *ApprovalBeforeTool) consumeApproval(sessionID string, use model.ToolUse) (bool, map[string]any) {
	toolName := use.Name
	if p == nil || sessionID == "" || toolName == "" {
		return false, nil
	}
	inputHash := ""
	if p.bindInput {
		var err error
		inputHash, err = hashToolInput(use.Input)
		if err != nil {
			return false, nil
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	grants := p.approved[sessionID][toolName]
	for i, grant := range grants {
		if p.bindInput && grant.InputHash != inputHash {
			continue
		}
		if p.singleUse {
			grants = append(grants[:i], grants[i+1:]...)
			if len(grants) == 0 {
				delete(p.approved[sessionID], toolName)
			} else {
				p.approved[sessionID][toolName] = grants
			}
		}
		metadata := map[string]any{
			model.MetadataApprovalConsumed:  true,
			model.MetadataApprovalAction:    toolName,
			model.MetadataApprovalSingleUse: p.singleUse,
		}
		if grant.InputHash != "" {
			metadata[model.MetadataApprovalInputHash] = grant.InputHash
		}
		return true, metadata
	}
	return false, nil
}

func (p *ApprovalBeforeTool) approvalName() string {
	if p == nil || strings.TrimSpace(p.approvalToolName) == "" {
		return approvaltools.ToolName
	}
	return p.approvalToolName
}

// ApprovalBeforeToolReason returns the model-visible denial reason used by
// RequireApprovalBeforeTools.
func ApprovalBeforeToolReason(toolName string) string {
	return "request approval for " + strings.TrimSpace(toolName) + " before using it"
}

// Options returns hook options for command allow/deny gating.
func (p *CommandPolicy) Options() []hook.Option {
	return []hook.Option{hook.WithBeforeToolUse(p.BeforeToolUse)}
}

// BeforeToolUse applies command allow/deny rules to run_command calls.
func (p *CommandPolicy) BeforeToolUse(ctx context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeToolUseResult{}, err
	}
	if p == nil || input.Use.Name != p.commandToolName() {
		return hook.BeforeToolUseResult{}, nil
	}
	command := commandFromInput(input.Use.Input)
	if len(command.argv) == 0 {
		return hook.BeforeToolUseResult{DenyReason: "command input must include command"}, nil
	}
	if command.unsafeShell && (len(p.allow) > 0 || len(p.deny) > 0) {
		return hook.BeforeToolUseResult{DenyReason: CommandShellSyntaxReason(command.argv)}, nil
	}
	if commandMatchesAny(command.argv, p.deny) {
		return hook.BeforeToolUseResult{DenyReason: CommandDeniedReason(command.argv)}, nil
	}
	if len(p.allow) > 0 && !commandMatchesAny(command.argv, p.allow) {
		return hook.BeforeToolUseResult{DenyReason: CommandNotAllowedReason(command.argv)}, nil
	}
	return hook.BeforeToolUseResult{}, nil
}

func (p *CommandPolicy) commandToolName() string {
	if p == nil || strings.TrimSpace(p.toolName) == "" {
		return commandtools.ToolName
	}
	return p.toolName
}

// CommandDeniedReason returns the model-visible reason used by DenyCommands.
func CommandDeniedReason(argv []string) string {
	return "command is denied by policy: " + strings.Join(normalizeCommandArgs(argv), " ")
}

// CommandNotAllowedReason returns the model-visible reason used by AllowCommands.
func CommandNotAllowedReason(argv []string) string {
	return "command is not in the allowed command policy: " + strings.Join(normalizeCommandArgs(argv), " ")
}

// CommandShellSyntaxReason returns the model-visible reason used when command
// prefix policies cannot safely match shell control syntax.
func CommandShellSyntaxReason(argv []string) string {
	return "command contains shell control syntax that command policy cannot safely match: " + strings.Join(normalizeCommandArgs(argv), " ")
}

// Options returns hook options for command approval gating.
func (p *CommandApprovalPolicy) Options() []hook.Option {
	return []hook.Option{
		hook.WithBeforeToolUse(p.BeforeToolUse),
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// BeforeToolUse denies matching commands until approval has been observed.
func (p *CommandApprovalPolicy) BeforeToolUse(ctx context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeToolUseResult{}, err
	}
	if p == nil || input.Use.Name != p.commandName() {
		return hook.BeforeToolUseResult{}, nil
	}
	command := commandFromInput(input.Use.Input)
	if len(command.argv) == 0 || len(p.matchers) == 0 {
		return hook.BeforeToolUseResult{}, nil
	}
	if !command.unsafeShell && !commandMatchesAny(command.argv, p.matchers) {
		return hook.BeforeToolUseResult{}, nil
	}
	if consumed, metadata := p.consumeApproval(input.SessionID, input.Use); consumed {
		return hook.BeforeToolUseResult{Metadata: metadata}, nil
	}
	return hook.BeforeToolUseResult{DenyReason: ApprovalBeforeCommandReason(command.argv)}, nil
}

// AfterToolUse records granted command approvals from approval tool results.
func (p *CommandApprovalPolicy) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || input.SessionID == "" || input.Result.IsError || input.Use.Name != p.approvalName() {
		return nil
	}
	if operation, _ := input.Result.Metadata[approvaltools.MetadataApprovalOperation].(string); operation != "request" {
		return nil
	}
	approved, _ := input.Result.Metadata[approvaltools.MetadataApprovalApproved].(bool)
	action, _ := input.Result.Metadata[approvaltools.MetadataApprovalAction].(string)
	if !approved || strings.TrimSpace(action) != p.commandName() {
		return nil
	}
	inputHash, _ := input.Result.Metadata[approvaltools.MetadataApprovalInputHash].(string)
	inputHash = strings.TrimSpace(inputHash)
	if p.bindInput && inputHash == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approved == nil {
		p.approved = map[string][]approvalGrant{}
	}
	p.approved[input.SessionID] = append(p.approved[input.SessionID], approvalGrant{InputHash: inputHash})
	return nil
}

// SessionEnded removes command approval state for the completed session.
func (p *CommandApprovalPolicy) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes command approval state for sessionID.
func (p *CommandApprovalPolicy) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.approved, sessionID)
}

func (p *CommandApprovalPolicy) consumeApproval(sessionID string, use model.ToolUse) (bool, map[string]any) {
	if p == nil || sessionID == "" {
		return false, nil
	}
	inputHash := ""
	if p.bindInput {
		var err error
		inputHash, err = hashToolInput(use.Input)
		if err != nil {
			return false, nil
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	grants := p.approved[sessionID]
	for i, grant := range grants {
		if p.bindInput && grant.InputHash != inputHash {
			continue
		}
		if p.singleUse {
			grants = append(grants[:i], grants[i+1:]...)
			if len(grants) == 0 {
				delete(p.approved, sessionID)
			} else {
				p.approved[sessionID] = grants
			}
		}
		metadata := map[string]any{
			model.MetadataApprovalConsumed:  true,
			model.MetadataApprovalAction:    p.commandName(),
			model.MetadataApprovalSingleUse: p.singleUse,
		}
		if grant.InputHash != "" {
			metadata[model.MetadataApprovalInputHash] = grant.InputHash
		}
		return true, metadata
	}
	return false, nil
}

func (p *CommandApprovalPolicy) commandName() string {
	if p == nil || strings.TrimSpace(p.commandToolName) == "" {
		return commandtools.ToolName
	}
	return p.commandToolName
}

func (p *CommandApprovalPolicy) approvalName() string {
	if p == nil || strings.TrimSpace(p.approvalToolName) == "" {
		return approvaltools.ToolName
	}
	return p.approvalToolName
}

// ApprovalBeforeCommandReason returns the model-visible denial reason used by
// RequireApprovalBeforeCommands.
func ApprovalBeforeCommandReason(argv []string) string {
	return "request approval before running command: " + strings.Join(normalizeCommandArgs(argv), " ")
}

func hashToolInput(input json.RawMessage) (string, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// RequireVerificationBeforeFinal returns a policy that can be installed into a
// hook runner with hook.NewRunner(policy.Options()...). It treats successful
// mutating workspace patches and restores as requiring verification; successful
// verification clears the requirement for that session.
func RequireVerificationBeforeFinal() *VerifyBeforeFinal {
	return &VerifyBeforeFinal{dirty: map[string]bool{}}
}

// Options returns hook options for tracking workspace verification readiness
// and denying premature final answers.
func (p *VerifyBeforeFinal) Options() []hook.Option {
	return []hook.Option{
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithBeforeFinal(p.BeforeFinal),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// AfterToolUse records workspace mutation and verification results.
func (p *VerifyBeforeFinal) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.SessionID == "" || input.Result.IsError {
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataWorkspaceOperation].(string); isVerificationRequiredWorkspaceOperation(operation, input.Result.Metadata) {
		p.setDirty(input.SessionID, true)
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataVerificationOperation].(string); operation == "verify" {
		if passed, _ := input.Result.Metadata[model.MetadataVerificationPassed].(bool); passed {
			p.setDirty(input.SessionID, false)
		}
	}
	return nil
}

// BeforeFinal denies finalization when the current session has unverified
// workspace mutations.
func (p *VerifyBeforeFinal) BeforeFinal(ctx context.Context, input hook.BeforeFinalInput) (hook.BeforeFinalResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeFinalResult{}, err
	}
	if p.isDirty(input.SessionID) {
		return hook.BeforeFinalResult{DenyReason: verifyBeforeFinalReason}, nil
	}
	return hook.BeforeFinalResult{}, nil
}

// SessionEnded removes verification state for the completed session.
func (p *VerifyBeforeFinal) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes verification state for sessionID. It is safe to call on a nil
// policy or with an empty session ID.
func (p *VerifyBeforeFinal) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dirty, sessionID)
}

func (p *VerifyBeforeFinal) setDirty(sessionID string, dirty bool) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirty == nil {
		p.dirty = map[string]bool{}
	}
	if dirty {
		p.dirty[sessionID] = true
		return
	}
	delete(p.dirty, sessionID)
}

func (p *VerifyBeforeFinal) isDirty(sessionID string) bool {
	if p == nil || sessionID == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dirty[sessionID]
}

func isVerificationRequiredWorkspaceOperation(operation string, metadata map[string]any) bool {
	switch operation {
	case "restore":
		return true
	case "patch":
		dryRun, _ := metadata["dry_run"].(bool)
		return !dryRun
	default:
		return false
	}
}

type parsedCommand struct {
	argv        []string
	unsafeShell bool
}

func commandFromInput(input json.RawMessage) parsedCommand {
	var value struct {
		Command any `json:"command"`
	}
	if err := json.Unmarshal(input, &value); err != nil {
		return parsedCommand{}
	}
	return commandFromValue(value.Command)
}

func metadataCommand(metadata map[string]any) parsedCommand {
	if command, _ := metadata[model.MetadataCommandString].(string); strings.TrimSpace(command) != "" {
		return commandFromValue(command)
	}
	return commandFromValue(metadata[model.MetadataCommandArgv])
}

func commandFromValue(value any) parsedCommand {
	switch values := value.(type) {
	case string:
		return parsedCommand{
			argv:        normalizeCommandArgs(strings.Fields(values)),
			unsafeShell: hasShellControlSyntax(values),
		}
	case []string:
		return parsedCommand{argv: normalizeCommandArgs(values)}
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if str, ok := value.(string); ok {
				out = append(out, str)
			}
		}
		return parsedCommand{argv: normalizeCommandArgs(out)}
	default:
		return parsedCommand{}
	}
}

func hasShellControlSyntax(command string) bool {
	for _, marker := range []string{"&&", "||", ";", "|", "`", "$(", ">", "<", "\n", "\r"} {
		if strings.Contains(command, marker) {
			return true
		}
	}
	fields := strings.Fields(command)
	for _, field := range fields {
		if field == "&" {
			return true
		}
	}
	return false
}

func normalizeCommandArgs(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			out = append(out, arg)
		}
	}
	return out
}

func cloneCommandMatchers(matchers []CommandMatcher) []CommandMatcher {
	if len(matchers) == 0 {
		return nil
	}
	out := make([]CommandMatcher, 0, len(matchers))
	for _, matcher := range matchers {
		matcher.Prefix = normalizeCommandArgs(matcher.Prefix)
		if len(matcher.Prefix) == 0 {
			continue
		}
		out = append(out, matcher)
	}
	return out
}

func commandMatchesAny(argv []string, matchers []CommandMatcher) bool {
	if len(matchers) == 0 {
		return false
	}
	for _, matcher := range matchers {
		if commandMatches(argv, matcher) {
			return true
		}
	}
	return false
}

func commandMatches(argv []string, matcher CommandMatcher) bool {
	argv = normalizeCommandArgs(argv)
	prefix := normalizeCommandArgs(matcher.Prefix)
	if len(prefix) == 0 || len(argv) < len(prefix) {
		return false
	}
	for i := range prefix {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}

// VerifyBeforeFinalReason returns the model-visible denial reason used by
// RequireVerificationBeforeFinal. It is exported for tests and host UI copy.
func VerifyBeforeFinalReason() string {
	return verifyBeforeFinalReason
}

// RequireVerificationAfterCommands returns a policy that marks the session
// dirty after successful matching commands and denies finalization until a
// successful workspace verification result is observed.
func RequireVerificationAfterCommands(matchers ...CommandMatcher) *VerifyAfterCommands {
	return &VerifyAfterCommands{
		dirty:           map[string]bool{},
		matchers:        cloneCommandMatchers(matchers),
		commandToolName: commandtools.ToolName,
	}
}

// Options returns hook options for command verification readiness.
func (p *VerifyAfterCommands) Options() []hook.Option {
	return []hook.Option{
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithBeforeFinal(p.BeforeFinal),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// AfterToolUse records successful matching commands and verification results.
func (p *VerifyAfterCommands) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || input.SessionID == "" {
		return nil
	}
	if input.Use.Name == p.commandName() && !input.Result.IsError {
		command := metadataCommand(input.Result.Metadata)
		if len(p.matchers) > 0 && (command.unsafeShell || commandMatchesAny(command.argv, p.matchers)) {
			p.setDirty(input.SessionID, true)
		}
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataVerificationOperation].(string); operation == "verify" {
		if passed, _ := input.Result.Metadata[model.MetadataVerificationPassed].(bool); passed {
			p.setDirty(input.SessionID, false)
		}
	}
	return nil
}

// BeforeFinal denies finalization when matching commands have not been followed
// by successful verification.
func (p *VerifyAfterCommands) BeforeFinal(ctx context.Context, input hook.BeforeFinalInput) (hook.BeforeFinalResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeFinalResult{}, err
	}
	if p.isDirty(input.SessionID) {
		return hook.BeforeFinalResult{DenyReason: verifyBeforeFinalAfterCommandReason}, nil
	}
	return hook.BeforeFinalResult{}, nil
}

// SessionEnded removes command verification state for the completed session.
func (p *VerifyAfterCommands) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes command verification state for sessionID.
func (p *VerifyAfterCommands) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dirty, sessionID)
}

func (p *VerifyAfterCommands) setDirty(sessionID string, dirty bool) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirty == nil {
		p.dirty = map[string]bool{}
	}
	if dirty {
		p.dirty[sessionID] = true
		return
	}
	delete(p.dirty, sessionID)
}

func (p *VerifyAfterCommands) isDirty(sessionID string) bool {
	if p == nil || sessionID == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dirty[sessionID]
}

func (p *VerifyAfterCommands) commandName() string {
	if p == nil || strings.TrimSpace(p.commandToolName) == "" {
		return commandtools.ToolName
	}
	return p.commandToolName
}

// VerifyAfterCommandReason returns the model-visible denial reason used by
// RequireVerificationAfterCommands.
func VerifyAfterCommandReason() string {
	return verifyBeforeFinalAfterCommandReason
}

// RecommendRollbackOnFailedVerification returns a rollback recommendation
// policy. Install Options() into the hook runner and wrap the verification
// tool's verifier with WrapVerifier.
func RecommendRollbackOnFailedVerification() *RollbackOnFailedVerification {
	return &RollbackOnFailedVerification{checkpoints: map[string]string{}}
}

// Options returns hook options for recording checkpoint lifecycle state.
func (p *RollbackOnFailedVerification) Options() []hook.Option {
	return []hook.Option{
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// WrapVerifier returns a verifier that appends rollback guidance to failed
// verification results when this policy has observed a checkpoint for the
// verification request's session.
func (p *RollbackOnFailedVerification) WrapVerifier(verifier verifytools.Verifier) verifytools.Verifier {
	return verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		if verifier == nil {
			return verifytools.Result{}, fmt.Errorf("agentpolicy: verifier is required")
		}
		result, err := verifier.Verify(ctx, req)
		if err != nil || result.Passed {
			return result, err
		}
		checkpointID := p.latestCheckpoint(req.SessionID)
		if checkpointID == "" {
			return result, nil
		}
		metadata := model.CloneMetadata(result.Metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata[MetadataRollbackRecommended] = true
		metadata[MetadataRollbackCheckpointID] = checkpointID
		result.Metadata = metadata
		instruction := fmt.Sprintf("Rollback policy: restore workspace checkpoint %s before continuing, then repair and verify again.", checkpointID)
		if result.Output == "" {
			result.Output = instruction
		} else {
			result.Output += "\n" + instruction
		}
		return result, nil
	})
}

// AfterToolUse records successful workspace checkpoints. Auto-checkpointing
// patch tools report the checkpoint ID on the patch result so rollback guidance
// remains available without requiring the model to call workspace_checkpoint
// explicitly before every edit.
func (p *RollbackOnFailedVerification) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.Result.IsError {
		return nil
	}
	checkpointID, _ := input.Result.Metadata[model.MetadataWorkspaceCheckpointID].(string)
	if input.SessionID == "" || checkpointID == "" {
		return nil
	}
	operation, _ := input.Result.Metadata[model.MetadataWorkspaceOperation].(string)
	autoCheckpoint, _ := input.Result.Metadata[model.MetadataWorkspaceAutoCheckpoint].(bool)
	switch {
	case input.Use.Name == workspacetools.CheckpointToolName && operation == "checkpoint":
	case input.Use.Name == workspacetools.ApplyPatchToolName && operation == "patch" && autoCheckpoint:
	default:
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkpoints == nil {
		p.checkpoints = map[string]string{}
	}
	p.checkpoints[input.SessionID] = checkpointID
	return nil
}

// SessionEnded removes checkpoint state for the completed session.
func (p *RollbackOnFailedVerification) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes rollback checkpoint state for sessionID. It is safe to call on
// a nil policy or with an empty session ID.
func (p *RollbackOnFailedVerification) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.checkpoints, sessionID)
}

func (p *RollbackOnFailedVerification) latestCheckpoint(sessionID string) string {
	if p == nil || sessionID == "" {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.checkpoints[sessionID]
}
