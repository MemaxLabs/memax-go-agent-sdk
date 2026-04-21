// Package scenarios provides reusable deterministic eval cases for core agent
// behaviors.
package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/skilltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

// All returns the default deterministic autonomy scenario suite. Returned cases
// are single-use because they contain stateful scripted models.
func All() []agenteval.Case {
	return []agenteval.Case{
		ToolRecovery(),
		StructuredOutputRepair(),
		MemorySearchAndSave(),
		MemoryDistillationCandidates(),
		MemoryCandidateHandlerWrites(),
		SessionResume(),
		ContextRetry(),
		SubagentDelegation(),
		SubagentScopedPlanProgress(),
		PlannerGuidedToolUse(),
		PlannerTaskStateUpdates(),
		ProgressiveSkillDisclosure(),
		ProgressiveSkillResourceLoading(),
		ProgressiveLargeSkillCatalog(),
		ProgressiveSkillSearchRecovery(),
		ContextPreservesLoadedSkill(),
		ContextCompactionProvenance(),
		WorkspacePatchCheckpointRollback(),
		WorkspaceUnifiedDiffRecovery(),
		WorkspacePatchReviewDenialRecovery(),
		WorkspaceOSStorePatchRollback(),
		WorkspaceGitStorePatchRollback(),
		WorkspaceCheckpointPolicyRecovery(),
		WorkspaceRollbackPolicyRecovery(),
		WorkspaceVerifyBeforeFinalPolicyRecovery(),
		WorkspaceApprovalPolicyRecovery(),
		WorkspaceApprovalDeniedFallback(),
		PlannerVerificationGuidesRepair(),
		PlannerTaskProgressFromVerification(),
		PlannerWorkspaceCommandRepairLoop(),
		PersonalPresetAssistant(),
		PersonalPresetAssistantMemoryApprovalRecovery(),
		PersonalPresetAssistantNoteRecall(),
		PersonalPresetAssistantMessageRecall(),
		PersonalPresetAssistantMessageApprovalRecovery(),
		PersonalPresetAssistantInboxTriageReplyFollowup(),
		PersonalPresetAssistantInboxSendBackendFailure(),
		PersonalPresetAssistantJMAPInboxReply(),
		PersonalPresetAssistantScheduleRecall(),
		PersonalPresetAssistantScheduleApprovalRecovery(),
		PersonalPresetAssistantScheduleConflictRecovery(),
		PersonalPresetAssistantDailyBriefing(),
		PersonalPresetAssistantWeekAheadPlanning(),
		PersonalPresetAssistantWeekAheadTaskLedger(),
		PersonalPresetAssistantWeekAheadTaskLedgerSQLite(),
		PersonalPresetAssistantScheduledDailyBriefing(),
		PersonalPresetAssistantScheduledDailyBriefingNotification(),
		PersonalPresetAssistantScheduledNotificationDeliveryRetry(),
		PersonalPresetAssistantScheduledRunStaleReconciliation(),
		PersonalPresetAssistantScheduledTaskLedgerMaintenance(),
		PersonalPresetAssistantScheduledInboxTriage(),
		PersonalPresetAssistantScheduledInboxTriageJMAP(),
		PersonalPresetResearchPartner(),
		CloudManagedPresetManagedWorkerQuotaDenial(),
		CloudManagedPresetManagedWorkerDelegatedAuditTrail(),
		CloudManagedPresetManagedWorkerAsyncAuditBackpressure(),
		CloudManagedPresetManagedWorkerDurableRunLifecycle(),
		CloudManagedPresetManagedWorkerExecuteQueuedRun(),
		CloudManagedPresetManagedWorkerRemoteHTTPPoll(),
		CloudManagedPresetManagedWorkerRemoteHTTPStaleFailure(),
		CloudManagedPresetManagedWorkerTenantRevocation(),
		CodingPresetSafeLocal(),
		CodingPresetSafeLocalRollbackRecovery(),
		CodingPresetCIRepair(),
		CodingPresetCIRepairApprovalRecovery(),
		CodingPresetInteractiveDev(),
		CodingPresetInteractiveDevWaitRepair(),
		CodingPresetInteractiveDevSessionCleanup(),
		WorkspaceVerificationRepair(),
		WorkspaceVerificationRollback(),
		CommandTestRepairLoop(),
		CommandSessionRepairLoop(),
		CommandSessionWaitRepairLoop(),
		CommandSessionInteractiveRepairLoop(),
		CommandSessionTTYResize(),
		CommandApprovalPolicyRecovery(),
		CommandVerifyBeforeFinalPolicyRecovery(),
		OpenAIProviderTextAndUsage(),
		AnthropicProviderTextAndUsage(),
		OpenAIProviderToolUseRoundTrip(),
		AnthropicProviderToolUseRoundTrip(),
		PermissionDenialRecovery(),
		HookDenialRecovery(),
		LargeResultStorageRecovery(),
		BudgetStopsBeforeSecondModelCall(),
		BudgetStopsBeforeToolBatch(),
		BudgetStopsAfterTokenUsage(),
		FinalizationPolicyExhaustion(),
		DeferredToolDiscoveryRecovery(),
		StreamingSafeToolOverlap(),
		StreamingMutatingToolWaits(),
		StreamingPermissionDenialRecovery(),
		StreamingFailureCancelsEarlyTool(),
		StreamingCancellation(),
	}
}

// ProgressiveLargeSkillCatalog returns a single-use scenario where progressive
// disclosure keeps a large relevant catalog bounded while the model can still
// load the exact skill and supporting resource it needs.
func ProgressiveLargeSkillCatalog() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "skill-1",
				Name:  skill.LoadToolName,
				Input: json.RawMessage(`{"name":"database-review"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "resource-1",
				Name:  skill.ResourceToolName,
				Input: json.RawMessage(`{"skill_name":"database-review","resource":"migration-checklist"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Large catalog skill loaded with checklist."}},
	)
	skills, resources := largeProgressiveCatalog()

	return agenteval.Case{
		Name:   "progressive_large_skill_catalog",
		Prompt: "Review the database migration checklist with the right specialist skill.",
		Options: memaxagent.Options{
			Model:               modelClient,
			SkillSource:         skill.StaticSource(skills),
			SkillResourceSource: skill.StaticResourceSource(resources),
			SkillDisclosure:     skill.DisclosureProgressive,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(skill.LoadToolName),
			agenteval.ToolUsed(skill.ResourceToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Large catalog skill loaded with checklist."),
			{
				Name: "large catalog discovery is bounded metadata",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) < 1 {
						return fmt.Errorf("model requests = %d, want at least 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"database-review", "migration-checklist", skill.LoadToolName, skill.ResourceToolName} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("prompt missing %q:\n%s", want, prompt)
						}
					}
					for _, unexpected := range []string{"migration-helper-020", "Full database migration instructions", "Checklist body: verify rollback"} {
						if strings.Contains(prompt, unexpected) {
							return fmt.Errorf("prompt leaked or over-selected %q:\n%s", unexpected, prompt)
						}
					}
					if got := strings.Count(prompt, "\n\n- "); got != 8 {
						return fmt.Errorf("discovered skill count = %d, want 8:\n%s", got, prompt)
					}
					return nil
				},
			},
			{
				Name: "large catalog resources load on demand",
				Check: func(result agenteval.Result) error {
					loadedSkill := false
					loadedResource := false
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name == skill.LoadToolName && strings.Contains(toolResult.Content, "Full database migration instructions") {
							loadedSkill = true
						}
						if toolResult.Name == skill.ResourceToolName && strings.Contains(toolResult.Content, "Checklist body: verify rollback") {
							loadedResource = true
						}
					}
					if !loadedSkill || !loadedResource {
						return fmt.Errorf("tool results missing loaded skill/resource: %#v", result.ToolResults())
					}
					return nil
				},
			},
		},
	}
}

// ProgressiveSkillSearchRecovery returns a single-use scenario where a
// budget-limited progressive discovery prompt omits the needed skill, the model
// searches the catalog, then loads the omitted skill through the normal skill
// loader.
func ProgressiveSkillSearchRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  "search_skills",
				Input: json.RawMessage(`{"query":"semantic rollback hazards","limit":1}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "skill-1",
				Name:  skill.LoadToolName,
				Input: json.RawMessage(`{"name":"semantic-rollback-risk"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Recovered omitted skill through search."}},
	)
	skills := omittedSkillRecoveryCatalog()
	searchTool, searchErr := skilltools.NewSearchTool(skilltools.Config{
		Source: skill.StaticSource(skills),
	})

	return agenteval.Case{
		Name:   "progressive_skill_search_recovery",
		Prompt: "Review the database migration. Use skill search if the visible skill metadata is incomplete.",
		Options: memaxagent.Options{
			Model:           modelClient,
			Tools:           tool.NewRegistry(searchTool),
			SkillSource:     skill.StaticSource(skills),
			SkillDisclosure: skill.DisclosureProgressive,
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(searchErr),
			agenteval.ToolUsed("search_skills"),
			agenteval.ToolUsed(skill.LoadToolName),
			agenteval.EventKindEmitted(memaxagent.EventSkillDiscovery),
			agenteval.EventKindEmitted(memaxagent.EventSkillSearch),
			agenteval.EventKindEmitted(memaxagent.EventSkillLoaded),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Recovered omitted skill through search."),
			{
				Name: "initial discovery omitted recoverable skill",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) < 1 {
						return fmt.Errorf("model requests = %d, want at least 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					if strings.Contains(prompt, "semantic-rollback-risk") || strings.Contains(prompt, "Deep risk instructions") {
						return fmt.Errorf("initial prompt included omitted skill:\n%s", prompt)
					}
					if !strings.Contains(prompt, "omitted because the skill discovery budget was reached") {
						return fmt.Errorf("initial prompt missing omission note:\n%s", prompt)
					}
					return nil
				},
			},
			{
				Name: "search found and load returned omitted skill",
				Check: func(result agenteval.Result) error {
					found := false
					loaded := false
					for _, toolResult := range result.ToolResults() {
						switch toolResult.Name {
						case "search_skills":
							if strings.Contains(toolResult.Content, "semantic-rollback-risk") &&
								strings.Contains(toolResult.Content, "semantic rollback hazards") &&
								!strings.Contains(toolResult.Content, "Deep risk instructions") {
								found = true
							}
						case skill.LoadToolName:
							if strings.Contains(toolResult.Content, "Deep risk instructions") {
								loaded = true
							}
						}
					}
					if !found || !loaded {
						return fmt.Errorf("tool results missing search/load recovery: %#v", result.ToolResults())
					}
					return nil
				},
			},
		},
	}
}

// ContextCompactionProvenance returns a single-use scenario where a summarizing
// context policy emits compaction provenance and replaces a prior active
// summary instead of stacking summaries.
func ContextCompactionProvenance() agenteval.Case {
	store := session.NewMemoryStore()
	sess, createErr := store.Create(context.Background())
	appendErr := error(nil)
	if createErr == nil {
		appendErr = store.Append(context.Background(), sess.ID, model.Message{
			Role: model.RoleUser,
			Content: []model.ContentBlock{{
				Type: model.ContentText,
				Text: "S:old context",
			}},
			Metadata: map[string]any{contextwindow.MetadataContextSummary: true},
		})
		if appendErr == nil {
			appendErr = store.Append(context.Background(), sess.ID, textMessage(model.RoleUser, "old implementation notes"))
		}
	}
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "continued after compacted context"}},
	)

	return agenteval.Case{
		Name:   "context_compaction_provenance",
		Prompt: "continue with the latest work",
		Options: memaxagent.Options{
			Model:     modelClient,
			Sessions:  store,
			SessionID: sess.ID,
			Context: contextwindow.SummarizingBudget{
				MaxTokens:        24,
				MaxSummaryTokens: 18,
				SummaryPrefix:    "S:",
				Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
					return "new compacted context", nil
				}),
			},
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(createErr, appendErr),
			agenteval.FinalEquals("continued after compacted context"),
			agenteval.EventKindEmitted(memaxagent.EventContextCompacted),
			{
				Name: "compaction provenance and replacement",
				Check: func(result agenteval.Result) error {
					var record *contextwindow.CompactionRecord
					for _, event := range result.Events {
						if event.Kind == memaxagent.EventContextCompacted {
							record = event.Compaction
						}
					}
					if record == nil || record.SummaryHash == "" || record.ReplacedSummaries != 1 {
						return fmt.Errorf("compaction record = %#v, want summary hash and one replaced summary", record)
					}
					requests := modelClient.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("model requests = %d, want 1", len(requests))
					}
					summaryCount := 0
					for _, msg := range requests[0].Messages {
						if contextwindow.IsSummaryMessage(msg) {
							summaryCount++
						}
						if strings.Contains(msg.PlainText(), "old context") {
							return fmt.Errorf("old summary remained active: %#v", requests[0].Messages)
						}
					}
					if summaryCount != 1 {
						return fmt.Errorf("summary count = %d, want one active summary: %#v", summaryCount, requests[0].Messages)
					}
					return nil
				},
			},
		},
	}
}

// MemoryDistillationCandidates returns a single-use scenario where successful
// completion produces host-reviewable memory candidates without writing them.
func MemoryDistillationCandidates() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Rollback notes were added before merge."}},
	)
	store := memory.NewMemoryStore(nil)

	return agenteval.Case{
		Name:   "memory_distillation_candidates",
		Prompt: "Finish the migration review.",
		Options: memaxagent.Options{
			Model: modelClient,
			Planner: planner.Static(planner.Plan{
				Goal: "review migration",
				Steps: []planner.Step{{
					ID:     "task-1",
					Title:  "check rollback",
					Status: planner.StatusCompleted,
				}},
			}),
			MemoryDistiller: memory.RuleDistiller{{
				WhenResultContains: "rollback",
				WhenPlanContains:   "migration",
				Memory: memory.Memory{
					Name:    "migration-rollback",
					Scope:   memory.ScopeProject,
					Content: "Migration reviews require rollback notes.",
				},
				Reason:     "completed review established rollback requirement",
				Confidence: 0.9,
			}},
			MemorySource: store,
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals("Rollback notes were added before merge."),
			agenteval.EventKindEmitted(memaxagent.EventMemoryCandidates),
			requestCountEquals(modelClient, 1),
			{
				Name: "memory candidate emitted without write",
				Check: func(result agenteval.Result) error {
					candidates := result.MemoryCandidates()
					if len(candidates) != 1 || candidates[0].Memory.Name != "migration-rollback" {
						return fmt.Errorf("candidates = %#v, want migration rollback candidate", candidates)
					}
					items, err := store.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					if len(items) != 0 {
						return fmt.Errorf("stored memories = %#v, want no automatic writes", items)
					}
					return nil
				},
			},
		},
	}
}

// MemoryCandidateHandlerWrites returns a single-use scenario where a host
// candidate handler persists accepted post-result memory candidates.
func MemoryCandidateHandlerWrites() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Remember that database snapshots are required."}},
	)
	store := memory.NewMemoryStore(nil)

	return agenteval.Case{
		Name:   "memory_candidate_handler_writes",
		Prompt: "Finish the backup policy review.",
		Options: memaxagent.Options{
			Model: modelClient,
			MemoryDistiller: memory.StaticDistiller{{
				Memory: memory.Memory{
					Name:    "database-snapshots",
					Scope:   memory.ScopeProject,
					Content: "Database changes require a fresh snapshot before deployment.",
				},
				Reason:     "final answer established backup policy",
				Confidence: 0.95,
			}},
			MemoryCandidateHandler: memory.WriterHandler{
				Writer:        store,
				MinConfidence: 0.8,
				Scopes:        []memory.Scope{memory.ScopeProject},
			},
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals("Remember that database snapshots are required."),
			agenteval.EventKindEmitted(memaxagent.EventMemoryCandidates),
			requestCountEquals(modelClient, 1),
			{
				Name: "memory candidate persisted by handler",
				Check: func(result agenteval.Result) error {
					if candidates := result.MemoryCandidates(); len(candidates) != 1 || candidates[0].Memory.Name != "database-snapshots" {
						return fmt.Errorf("candidates = %#v, want database snapshot candidate", candidates)
					}
					items, err := store.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					if len(items) != 1 || items[0].Name != "database-snapshots" {
						return fmt.Errorf("stored memories = %#v, want persisted candidate", items)
					}
					return nil
				},
			},
		},
	}
}

// PlannerTaskStateUpdates returns a single-use scenario where tasktools are
// both model-editable state and the source for prompt-visible planner context.
func PlannerTaskStateUpdates() agenteval.Case {
	store := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:       "task-1",
		Title:    "read migration file",
		Status:   tasktools.StatusInProgress,
		Notes:    "check rollback",
		Priority: 1,
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"task-1",
					"status":"completed",
					"notes":"migration file reviewed"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "task plan updated"}},
	)

	return agenteval.Case{
		Name:   "planner_task_state_updates",
		Prompt: "Use the task plan and mark progress.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(
				tasktools.NewListTool(store),
				tasktools.NewUpsertTool(store),
			),
			Planner: tasktools.Planner(store,
				planner.WithTaskGoal("review migration safely"),
				planner.WithTaskToolHints(tasktools.ListToolName, tasktools.UpsertToolName),
			),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(tasktools.UpsertToolName),
			toolResultContains(tasktools.UpsertToolName, false, "upserted task-1"),
			agenteval.FinalEquals("task plan updated"),
			requestCountEquals(modelClient, 2),
			{
				Name: "plan reflected task update",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want 2", len(requests))
					}
					first := requests[0].SystemPrompt
					if !strings.Contains(first, "[in_progress] task-1: read migration file") || !strings.Contains(first, "check rollback") {
						return fmt.Errorf("first prompt missing initial task state:\n%s", first)
					}
					second := requests[1].SystemPrompt
					if !strings.Contains(second, "[completed] task-1: read migration file") || !strings.Contains(second, "migration file reviewed") {
						return fmt.Errorf("second prompt missing updated task state:\n%s", second)
					}
					return nil
				},
			},
		},
	}
}

// ProgressiveSkillDisclosure returns a single-use scenario where the prompt
// exposes only skill metadata, the model explicitly loads the skill, and the
// loaded instructions persist as a normal tool result.
func ProgressiveSkillDisclosure() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "skill-1",
				Name:  skill.LoadToolName,
				Input: json.RawMessage(`{"name":"database-review"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Migration reviewed with the database skill."}},
	)
	skills := skill.StaticSource{{
		Name:        "database-review",
		Description: "Review database migrations.",
		WhenToUse:   "SQL migrations or rollback plans are involved.",
		Tags:        []string{"database", "migration"},
		AlwaysOn:    true,
		Content:     "Check lock behavior, rollback safety, and data backfill risk.",
	}}

	return agenteval.Case{
		Name:   "progressive_skill_disclosure",
		Prompt: "Review the SQL migration with the right skill.",
		Options: memaxagent.Options{
			Model:           modelClient,
			SkillSource:     skills,
			SkillDisclosure: skill.DisclosureProgressive,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(skill.LoadToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Migration reviewed with the database skill."),
			{
				Name: "prompt exposes metadata only",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) < 1 {
						return fmt.Errorf("model requests = %d, want at least 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					if !strings.Contains(prompt, "database-review") || !strings.Contains(prompt, skill.LoadToolName) {
						return fmt.Errorf("prompt missing skill metadata or load tool:\n%s", prompt)
					}
					if strings.Contains(prompt, "Check lock behavior") {
						return fmt.Errorf("prompt leaked full skill instructions:\n%s", prompt)
					}
					return nil
				},
			},
			{
				Name: "loaded skill result contains instructions",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name == skill.LoadToolName && strings.Contains(toolResult.Content, "rollback safety") {
							return nil
						}
					}
					return fmt.Errorf("load_skill result did not contain full instructions: %#v", result.ToolResults())
				},
			},
		},
	}
}

// ProgressiveSkillResourceLoading returns a single-use scenario where the
// prompt exposes supporting skill resource metadata, the model loads one
// resource through a tool, and the full resource content stays out of the
// initial prompt.
func ProgressiveSkillResourceLoading() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "skill-1",
				Name:  skill.LoadToolName,
				Input: json.RawMessage(`{"name":"database-review"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "resource-1",
				Name:  skill.ResourceToolName,
				Input: json.RawMessage(`{"skill_name":"database-review","resource":"migration-checklist"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Migration reviewed with the resource checklist."}},
	)
	skills := skill.StaticSource{{
		Name:        "database-review",
		Description: "Review database migrations.",
		WhenToUse:   "SQL migrations or rollback plans are involved.",
		Tags:        []string{"database", "migration"},
		AlwaysOn:    true,
		Content:     "Check lock behavior, rollback safety, and data backfill risk.",
		Resources: []skill.ResourceRef{{
			Name:        "migration-checklist",
			Description: "Checklist for migration rollout and rollback.",
			Path:        "resources/migration-checklist.md",
			MIMEType:    "text/markdown",
			Bytes:       256,
		}},
	}}
	resources := skill.StaticResourceSource{{
		SkillName: "database-review",
		Name:      "migration-checklist",
		Path:      "resources/migration-checklist.md",
		MIMEType:  "text/markdown",
		Content:   "Step 1: confirm rollback. Step 2: verify locks.",
	}}

	return agenteval.Case{
		Name:   "progressive_skill_resource_loading",
		Prompt: "Review the SQL migration with the right skill and checklist.",
		Options: memaxagent.Options{
			Model:               modelClient,
			SkillSource:         skills,
			SkillResourceSource: resources,
			SkillDisclosure:     skill.DisclosureProgressive,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(skill.LoadToolName),
			agenteval.ToolUsed(skill.ResourceToolName),
			agenteval.EventKindEmitted(memaxagent.EventSkillDiscovery),
			agenteval.EventKindEmitted(memaxagent.EventSkillLoaded),
			agenteval.EventKindEmitted(memaxagent.EventSkillResourceLoaded),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Migration reviewed with the resource checklist."),
			{
				Name: "prompt exposes resource metadata only",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) < 1 {
						return fmt.Errorf("model requests = %d, want at least 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"database-review", "migration-checklist", skill.ResourceToolName} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("prompt missing %q:\n%s", want, prompt)
						}
					}
					if strings.Contains(prompt, "Step 1: confirm rollback") {
						return fmt.Errorf("prompt leaked resource content:\n%s", prompt)
					}
					return nil
				},
			},
			{
				Name: "loaded resource result contains content",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name == skill.ResourceToolName && strings.Contains(toolResult.Content, "verify locks") {
							if toolResult.Metadata[model.MetadataLoadedSkillResource] != true {
								return fmt.Errorf("resource metadata = %#v, want marker", toolResult.Metadata)
							}
							return nil
						}
					}
					return fmt.Errorf("read_skill_resource result did not contain full resource: %#v", result.ToolResults())
				},
			},
		},
	}
}

func largeProgressiveCatalog() ([]skill.Skill, []skill.Resource) {
	skills := []skill.Skill{{
		Name:        "database-review",
		Description: "Review database migrations.",
		WhenToUse:   "Use for database migration checklist, rollback, lock, and backfill reviews.",
		Tags:        []string{"database", "migration", "rollback"},
		Content:     "Full database migration instructions: check locks, rollback safety, and backfill windows.",
		Resources: []skill.ResourceRef{{
			Name:        "migration-checklist",
			Description: "Database migration rollout checklist.",
			Path:        "resources/migration-checklist.md",
			MIMEType:    "text/markdown",
			Bytes:       512,
		}},
	}}
	for i := 0; i < 32; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("migration-helper-%03d", i),
			Description: fmt.Sprintf("Database migration helper %03d.", i),
			WhenToUse:   "Use for database migration review subtasks.",
			Tags:        []string{"database", "migration"},
			Content:     fmt.Sprintf("Full helper instructions %03d.", i),
		})
	}
	resources := []skill.Resource{{
		SkillName: "database-review",
		Name:      "migration-checklist",
		Path:      "resources/migration-checklist.md",
		MIMEType:  "text/markdown",
		Content:   "Checklist body: verify rollback, locks, backups, and monitoring.",
	}}
	return skills, resources
}

func omittedSkillRecoveryCatalog() []skill.Skill {
	skills := make([]skill.Skill, 0, 17)
	for i := 0; i < 16; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("migration-helper-%03d", i),
			Description: fmt.Sprintf("Database migration helper %03d.", i),
			WhenToUse:   "Use for database migration review.",
			Tags:        []string{"database", "migration"},
			Content:     fmt.Sprintf("Helper migration instructions %03d.", i),
		})
	}
	skills = append(skills, skill.Skill{
		Name:        "semantic-rollback-risk",
		Description: "Find semantic rollback hazards and hidden data coupling.",
		WhenToUse:   "Use for semantic rollback hazard reviews.",
		Tags:        []string{"rollback", "risk"},
		Content:     "Deep risk instructions: inspect semantic rollback hazards and hidden data coupling.",
	})
	return skills
}

// ContextPreservesLoadedSkill returns a single-use scenario where aggressive
// context trimming keeps a previously loaded progressive skill as a valid
// assistant tool-use plus tool-result group.
func ContextPreservesLoadedSkill() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "skill-1",
				Name:  skill.LoadToolName,
				Input: json.RawMessage(`{"name":"database-review"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "context kept the loaded skill"}},
	)
	skills := skill.StaticSource{{
		Name:        "database-review",
		Description: "Review database migrations.",
		WhenToUse:   "SQL migrations or rollback plans are involved.",
		AlwaysOn:    true,
		Content:     "Check lock behavior, rollback safety, and data backfill risk.",
	}}

	return agenteval.Case{
		Name:   "context_preserves_loaded_skill",
		Prompt: "Review the SQL migration with the right skill.",
		Options: memaxagent.Options{
			Model:           modelClient,
			SkillSource:     skills,
			SkillDisclosure: skill.DisclosureProgressive,
			Context: contextwindow.PreserveImportant{
				Policy: contextwindow.RecentMessages{MaxMessages: 1},
			},
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(skill.LoadToolName),
			agenteval.FinalEquals("context kept the loaded skill"),
			requestCountEquals(modelClient, 2),
			{
				Name: "trimmed context retained loaded skill group",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want 2", len(requests))
					}
					messages := requests[1].Messages
					if len(messages) != 2 {
						return fmt.Errorf("second request messages = %#v, want assistant tool use plus skill result", messages)
					}
					if !messageContainsToolUse(messages[0], "skill-1", skill.LoadToolName) {
						return fmt.Errorf("first retained message = %#v, want load_skill tool use", messages[0])
					}
					if messages[1].ToolResult == nil || messages[1].ToolResult.Name != skill.LoadToolName {
						return fmt.Errorf("second retained message = %#v, want load_skill result", messages[1])
					}
					if !strings.Contains(messages[1].ToolResult.Content, "rollback safety") {
						return fmt.Errorf("loaded skill content = %q, want instructions", messages[1].ToolResult.Content)
					}
					return nil
				},
			},
		},
	}
}

// PlannerGuidedToolUse returns a single-use scenario where host-provided plan
// context is injected into the prompt and the model follows the planned tool
// path.
func PlannerGuidedToolUse() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"migrations/001.sql"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "planner-guided review complete"}},
	)

	return agenteval.Case{
		Name:   "planner_guided_tool_use",
		Prompt: "Review the migration using the host plan.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(readFileTool()),
			Planner: planner.Static(planner.Plan{
				Goal:        "review migration safely",
				State:       planner.StateActive,
				Constraints: []string{"inspect the migration before judging risk"},
				Steps: []planner.Step{{
					ID:        "step-1",
					Title:     "read migration file",
					Status:    planner.StatusInProgress,
					ToolHints: []string{"read_file"},
					Evidence:  []string{"migrations/001.sql"},
				}},
			}),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			toolResultContains("read_file", false, "read migrations/001.sql"),
			agenteval.FinalEquals("planner-guided review complete"),
			requestCountEquals(modelClient, 2),
			{
				Name: "plan injected into prompt",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) == 0 {
						return fmt.Errorf("missing model request")
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"Host-provided plan", "review migration safely", "read_file", "migrations/001.sql"} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("system prompt missing %q:\n%s", want, prompt)
						}
					}
					return nil
				},
			},
		},
	}
}

// ToolRecovery returns a single-use scenario where the model emits invalid tool
// input, receives the validation error as a tool result, and recovers.
func ToolRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":42}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "recovered after tool validation"}},
	)
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "read_file",
			Description: "Read a file by path.",
			ReadOnly:    true,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "handler should not run"}, nil
		},
	})

	return agenteval.Case{
		Name:   "tool_recovery",
		Prompt: "Read README.md and recover if tool input is invalid.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: registry,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			agenteval.FinalEquals("recovered after tool validation"),
			{
				Name: "tool validation error surfaced",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.IsError && strings.Contains(toolResult.Content, "jsonschema") {
							return nil
						}
					}
					return fmt.Errorf("missing model-visible tool validation error")
				},
			},
			{
				Name: "model retried after tool error",
				Check: func(agenteval.Result) error {
					if got := len(modelClient.Requests()); got != 2 {
						return fmt.Errorf("model requests = %d, want 2", got)
					}
					return nil
				},
			},
		},
	}
}

// StructuredOutputRepair returns a single-use scenario where invalid final JSON
// is persisted, the SDK appends a repair prompt, and the model returns valid
// JSON.
func StructuredOutputRepair() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "not json"}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	)
	return agenteval.Case{
		Name:   "structured_output_repair",
		Prompt: "Return a structured answer.",
		Options: memaxagent.Options{
			Model:  modelClient,
			Output: answerContract(),
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals(`{"answer":"fixed"}`),
			{
				Name: "repair prompt visible on retry",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want 2", len(requests))
					}
					messages := requests[1].Messages
					if len(messages) < 3 {
						return fmt.Errorf("retry messages = %#v, want invalid answer and repair prompt", messages)
					}
					previous := messages[len(messages)-2]
					if previous.Role != model.RoleAssistant || previous.PlainText() != "not json" {
						return fmt.Errorf("previous retry message = %#v, want invalid assistant answer", previous)
					}
					last := messages[len(messages)-1]
					if last.Role != model.RoleUser || !strings.Contains(last.PlainText(), "structured output contract") {
						return fmt.Errorf("last retry message = %#v, want structured output repair prompt", last)
					}
					return nil
				},
			},
		},
	}
}

// MemorySearchAndSave returns a single-use scenario where the model searches
// durable memories, saves a new memory, and completes with the saved memory in
// the backing store.
func MemorySearchAndSave() agenteval.Case {
	store := memory.NewMemoryStore([]memory.Memory{{
		Name:    "billing-rule",
		Scope:   memory.ScopeProject,
		Content: "Invoices require audit logs.",
		Tags:    []string{"billing"},
	}})
	searchTool, searchErr := memorytools.NewSearchTool(memorytools.Config{Source: store})
	saveTool, saveErr := memorytools.NewSaveTool(memorytools.Config{Writer: store})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"invoice audit","limit":1}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-2",
				Name: memorytools.SaveToolName,
				Input: json.RawMessage(`{
					"name":"billing-rollback",
					"scope":"project",
					"description":"Billing rollback requirement",
					"content":"Billing changes require rollback notes.",
					"tags":["billing","rollback"]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "memory saved"}},
	)

	return agenteval.Case{
		Name:   "memory_search_and_save",
		Prompt: "Search billing memory, then save the rollback requirement.",
		Options: memaxagent.Options{
			Model:        modelClient,
			Tools:        tool.NewRegistry(searchTool, saveTool),
			MemorySource: store,
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(searchErr, saveErr),
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.ToolUsed(memorytools.SaveToolName),
			agenteval.FinalEquals("memory saved"),
			{
				Name: "search returned seeded memory",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name == memorytools.SearchToolName &&
							strings.Contains(toolResult.Content, "billing-rule") &&
							strings.Contains(toolResult.Content, "Invoices require audit logs") {
							return nil
						}
					}
					return fmt.Errorf("search result did not contain seeded billing memory: %#v", result.ToolResults())
				},
			},
			{
				Name: "memory persisted",
				Check: func(agenteval.Result) error {
					items, err := store.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					for _, item := range items {
						if item.Name == "billing-rollback" && strings.Contains(item.Content, "rollback notes") {
							return nil
						}
					}
					return fmt.Errorf("saved memory not found: %#v", items)
				},
			},
			agenteval.NoToolErrors(),
		},
	}
}

// SessionResume returns a single-use scenario where a run resumes an existing
// durable transcript and sends both previous and new user messages to the model.
func SessionResume() agenteval.Case {
	store := session.NewMemoryStore()
	sess, createErr := store.Create(context.Background())
	appendErr := error(nil)
	if createErr == nil {
		appendErr = store.Append(context.Background(), sess.ID, textMessage(model.RoleUser, "previous session context"))
	}
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "resumed session"}},
	)

	return agenteval.Case{
		Name:   "session_resume",
		Prompt: "continue from previous context",
		Options: memaxagent.Options{
			Model:     modelClient,
			Sessions:  store,
			SessionID: sess.ID,
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(createErr, appendErr),
			agenteval.FinalEquals("resumed session"),
			{
				Name: "resumed session id used",
				Check: func(result agenteval.Result) error {
					if result.SessionID != sess.ID {
						return fmt.Errorf("session id = %q, want %q", result.SessionID, sess.ID)
					}
					return nil
				},
			},
			{
				Name: "previous transcript sent",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("model requests = %d, want 1", len(requests))
					}
					messages := requests[0].Messages
					if len(messages) != 2 {
						return fmt.Errorf("messages = %#v, want previous and current user messages", messages)
					}
					if messages[0].PlainText() != "previous session context" || messages[1].PlainText() != "continue from previous context" {
						return fmt.Errorf("messages = %#v, want resumed transcript", messages)
					}
					return nil
				},
			},
		},
	}
}

// ContextRetry returns a single-use scenario where a context-window rejection
// triggers the configured retry policy and the compacted retry succeeds.
func ContextRetry() agenteval.Case {
	store := session.NewMemoryStore()
	sess, createErr := store.Create(context.Background())
	appendErr := error(nil)
	if createErr == nil {
		appendErr = store.Append(context.Background(), sess.ID, textMessage(model.RoleUser, "old context that should be dropped"))
	}
	modelClient := &contextRetryClient{
		success: agenteval.NewScriptedModel(
			[]model.StreamEvent{{Kind: model.StreamText, Text: "retried after context pressure"}},
		),
	}

	return agenteval.Case{
		Name:   "context_retry",
		Prompt: "current context retry request",
		Options: memaxagent.Options{
			Model:        modelClient,
			Sessions:     store,
			SessionID:    sess.ID,
			ContextRetry: contextwindow.RecentMessages{MaxMessages: 1},
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(createErr, appendErr),
			agenteval.FinalEquals("retried after context pressure"),
			{
				Name: "retry used compacted messages",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want failed request and retry", len(requests))
					}
					if got := len(requests[0].Messages); got != 2 {
						return fmt.Errorf("first request messages = %d, want full resumed transcript", got)
					}
					retryMessages := requests[1].Messages
					if len(retryMessages) != 1 || retryMessages[0].PlainText() != "current context retry request" {
						return fmt.Errorf("retry messages = %#v, want compacted current request only", retryMessages)
					}
					return nil
				},
			},
		},
	}
}

// SubagentDelegation returns a single-use scenario where a parent agent calls a
// bounded child agent through the normal tool layer and receives child session
// correlation metadata.
func SubagentDelegation() agenteval.Case {
	store := session.NewMemoryStore()
	childModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "child investigation complete"}},
	)
	delegate, delegateErr := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name:        "investigator",
			Description: "Investigates a focused question.",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
			},
		}},
		DefaultOptions: memaxagent.Options{Sessions: store},
	})
	parentModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-1",
				Name: "run_subagent",
				Input: json.RawMessage(`{
					"agent":"investigator",
					"prompt":"investigate the migration risk"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "parent received child result"}},
	)

	return agenteval.Case{
		Name:   "subagent_delegation",
		Prompt: "delegate a focused investigation",
		Options: memaxagent.Options{
			Model:    parentModel,
			Tools:    tool.NewRegistry(delegate),
			Sessions: store,
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(delegateErr),
			agenteval.ToolUsed("run_subagent"),
			agenteval.FinalEquals("parent received child result"),
			agenteval.NoToolErrors(),
			{
				Name: "subagent result metadata linked",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name != "run_subagent" {
							continue
						}
						if toolResult.Content != "child investigation complete" {
							return fmt.Errorf("subagent result content = %q, want child result", toolResult.Content)
						}
						if toolResult.Metadata["agent"] != "investigator" {
							return fmt.Errorf("subagent metadata = %#v, want agent", toolResult.Metadata)
						}
						if toolResult.Metadata["parent_session_id"] != result.SessionID {
							return fmt.Errorf("subagent metadata = %#v, want parent session %q", toolResult.Metadata, result.SessionID)
						}
						if child, _ := toolResult.Metadata["child_session_id"].(string); child == "" {
							return fmt.Errorf("subagent metadata = %#v, want child session id", toolResult.Metadata)
						}
						return nil
					}
					return fmt.Errorf("missing subagent tool result")
				},
			},
			{
				Name: "child model ran once",
				Check: func(agenteval.Result) error {
					if got := len(childModel.Requests()); got != 1 {
						return fmt.Errorf("child model requests = %d, want 1", got)
					}
					return nil
				},
			},
		},
	}
}

// SubagentScopedPlanProgress returns a single-use scenario where a parent
// delegates one task to a child agent, the child sees only the scoped plan, and
// the child result updates parent-visible task progress.
func SubagentScopedPlanProgress() agenteval.Case {
	store := session.NewMemoryStore()
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{
		{ID: "task-1", Title: "inspect migration rollback", Status: tasktools.StatusInProgress, Evidence: []string{"migrations/001.sql"}},
		{ID: "task-2", Title: "unrelated frontend cleanup", Status: tasktools.StatusPending},
	})
	childModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "child scoped task complete"}},
	)
	delegate, delegateErr := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name:        "investigator",
			Description: "Investigates a focused task.",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
			},
		}},
		DefaultOptions: memaxagent.Options{Sessions: store},
		PlanSource: tasktools.SubagentPlanner(taskStore,
			planner.WithTaskToolHints("read_file"),
			planner.WithTaskVerificationHints("report evidence before completion"),
		),
		ResultHandler: tasktools.NewSubagentProgressHandler(taskStore),
	})
	parentModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-1",
				Name: "run_subagent",
				Input: json.RawMessage(`{
					"agent":"investigator",
					"prompt":"inspect rollback safety for the migration task",
					"task_id":"task-1"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "parent completed delegated task"}},
	)

	return agenteval.Case{
		Name:   "subagent_scoped_plan_progress",
		Prompt: "Delegate task-1 and finish only after progress is updated.",
		Options: memaxagent.Options{
			Model:    parentModel,
			Tools:    tool.NewRegistry(delegate),
			Sessions: store,
			Planner: tasktools.Planner(taskStore,
				planner.WithTaskGoal("complete migration review tasks"),
				planner.WithTaskToolHints("run_subagent"),
			),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(delegateErr),
			agenteval.ToolUsed("run_subagent"),
			agenteval.FinalEquals("parent completed delegated task"),
			agenteval.NoToolErrors(),
			requestCountEquals(parentModel, 2),
			{
				Name: "child received scoped plan only",
				Check: func(agenteval.Result) error {
					requests := childModel.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("child requests = %d, want 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"complete delegated task task-1", "task-1", "inspect migration rollback", "migrations/001.sql", "report evidence before completion"} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("child prompt missing %q:\n%s", want, prompt)
						}
					}
					if strings.Contains(prompt, "task-2") || strings.Contains(prompt, "frontend cleanup") {
						return fmt.Errorf("child prompt leaked unrelated task:\n%s", prompt)
					}
					return nil
				},
			},
			{
				Name: "subagent result updates parent task progress",
				Check: func(result agenteval.Result) error {
					found := false
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name != "run_subagent" {
							continue
						}
						found = true
						if toolResult.Metadata[model.MetadataTaskID] != "task-1" || toolResult.Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
							return fmt.Errorf("subagent metadata = %#v, want completed task metadata", toolResult.Metadata)
						}
					}
					if !found {
						return fmt.Errorf("missing subagent result")
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 2 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want task-1 completed", tasks)
					}
					parentRequests := parentModel.Requests()
					if len(parentRequests) < 2 {
						return fmt.Errorf("parent requests = %d, want final request", len(parentRequests))
					}
					finalPrompt := parentRequests[1].SystemPrompt
					for _, want := range []string{"[completed] task-1", "subagent investigator completed", "subagent:investigator"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final parent prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					return nil
				},
			},
		},
	}
}

func toolConstructionSucceeded(errs ...error) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "tool construction succeeded",
		Check: func(agenteval.Result) error {
			for _, err := range errs {
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func setupSucceeded(errs ...error) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "setup succeeded",
		Check: func(agenteval.Result) error {
			for _, err := range errs {
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func answerContract() output.Contract {
	return output.Contract{Schema: map[string]any{
		"type":                 "object",
		"required":             []any{"answer"},
		"additionalProperties": false,
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}}
}

func textMessage(role model.Role, text string) model.Message {
	return model.Message{
		Role: role,
		Content: []model.ContentBlock{{
			Type: model.ContentText,
			Text: text,
		}},
	}
}

type contextRetryClient struct {
	mu             sync.Mutex
	failed         bool
	failedRequests []model.Request
	success        *agenteval.ScriptedModel
}

func (c *contextRetryClient) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	c.mu.Lock()
	if !c.failed {
		c.failed = true
		c.failedRequests = append(c.failedRequests, cloneRequest(req))
		c.mu.Unlock()
		return nil, model.ErrContextWindowExceeded
	}
	success := c.success
	c.mu.Unlock()
	return success.Stream(ctx, req)
}

func (c *contextRetryClient) Requests() []model.Request {
	c.mu.Lock()
	failed := make([]model.Request, len(c.failedRequests))
	for i, req := range c.failedRequests {
		failed[i] = cloneRequest(req)
	}
	success := c.success
	c.mu.Unlock()
	if success == nil {
		return failed
	}
	return append(failed, success.Requests()...)
}

func cloneRequest(req model.Request) model.Request {
	req.Messages = model.CloneMessages(req.Messages)
	req.Tools = cloneToolSpecs(req.Tools)
	return req
}

func cloneToolSpecs(specs []model.ToolSpec) []model.ToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		out[i].InputSchema = cloneSchemaMap(spec.InputSchema)
	}
	return out
}

func cloneSchemaMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneSchemaValue(item)
	}
	return out
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return typed
	}
}
