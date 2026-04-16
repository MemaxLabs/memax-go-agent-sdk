package prompt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
)

const (
	maxSelectorQueryMessages = 3
	maxSelectorQueryBytes    = 4096

	defaultProgressiveSkillLimit          = 8
	defaultProgressiveSkillDiscoveryBytes = 12 * 1024
)

// Builder builds the provider-facing system prompt for a model request.
type Builder interface {
	Build(context.Context, Request) (Result, error)
}

// BuilderFunc adapts a function to Builder.
type BuilderFunc func(context.Context, Request) (Result, error)

// Build calls f(ctx, req).
func (f BuilderFunc) Build(ctx context.Context, req Request) (Result, error) {
	return f(ctx, req)
}

// Request is the typed prompt input for one model request.
type Request struct {
	Identity           identity.Identity
	SystemPrompt       string
	AppendSystemPrompt string
	Messages           []model.Message
	Tools              []model.ToolSpec
	Plan               planner.Plan
	Memories           []memory.Memory
	Skills             []skill.Skill
	SkillDisclosure    skill.DisclosureMode
	SkillResources     bool
	OutputSchema       map[string]any
}

// Part is a named prompt component. Stable names make prompt snapshots and
// hashes easier to debug.
type Part struct {
	Name    string
	Content string
}

// Result is a built prompt plus prompt metadata.
type Result struct {
	SystemPrompt   string
	Parts          []Part
	Hash           string
	SkillDiscovery *SkillDiscovery
}

// SkillDiscovery describes progressive skill metadata included in a prompt.
type SkillDiscovery struct {
	SelectedSkills []string
	Selected       int
	Omitted        int
	PromptBytes    int
}

// DefaultBuilder is the SDK's default Memax-native prompt assembler. In
// progressive skill disclosure mode, a zero SkillSelector.MaxSkills uses a
// bounded discovery default. Direct skill injection keeps the selector's normal
// zero-value behavior and remains unbounded for compatibility with small,
// trusted skill sets.
type DefaultBuilder struct {
	SkillSelector skill.Selector
	// SkillDiscoveryMaxBytes bounds the memax.skill_discovery prompt part in
	// progressive skill disclosure mode. Zero uses the SDK default; negative
	// disables the byte budget. Direct skill injection is unaffected.
	SkillDiscoveryMaxBytes int
	MemorySelector         memory.Selector
	Profile                Profile
}

// Build assembles ordered prompt parts from identity, tools, skills, and host
// prompt text.
func (b DefaultBuilder) Build(ctx context.Context, req Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	var parts []Part
	parts = append(parts, Part{Name: "memax.core", Content: coreInstructions})
	parts = append(parts, Part{Name: "memax.identity", Content: formatIdentity(req.Identity.WithDefaults())})
	if len(req.Tools) > 0 {
		parts = append(parts, Part{Name: "memax.tool_guidance", Content: formatToolGuidance(req.Tools)})
	}
	if profile := b.Profile.WithDefault(); profile != ProfileGeneric {
		parts = append(parts, Part{Name: "memax.provider_profile", Content: formatProfile(profile)})
	}
	if len(req.OutputSchema) > 0 {
		parts = append(parts, Part{Name: "memax.output_contract", Content: formatOutputContract(req.OutputSchema)})
	}
	if !req.Plan.Empty() {
		parts = append(parts, Part{Name: "memax.plan", Content: formatPlan(req.Plan)})
	}
	if selected := b.MemorySelector.Select(req.Memories, requestQuery(req)); len(selected) > 0 {
		parts = append(parts, Part{Name: "memax.memories", Content: formatMemories(selected)})
	}
	var discovery *SkillDiscovery
	if selected, omitted := b.selectSkills(req.Skills, requestQuery(req), req.SkillDisclosure); len(selected) > 0 {
		if req.SkillDisclosure == skill.DisclosureProgressive {
			if content := formatSkillDiscovery(selected, req.SkillResources, b.skillDiscoveryMaxBytes(req.SkillDisclosure), omitted); content != "" {
				parts = append(parts, Part{Name: "memax.skill_discovery", Content: content})
				discovery = &SkillDiscovery{
					SelectedSkills: skillNames(selected),
					Selected:       countNamedSkills(selected),
					Omitted:        omitted,
					PromptBytes:    len(content),
				}
			}
		} else {
			parts = append(parts, Part{Name: "memax.skills", Content: formatSkills(selected)})
		}
	}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		parts = append(parts, Part{Name: "host.system", Content: strings.TrimSpace(req.SystemPrompt)})
	}
	if strings.TrimSpace(req.AppendSystemPrompt) != "" {
		parts = append(parts, Part{Name: "host.append_system", Content: strings.TrimSpace(req.AppendSystemPrompt)})
	}

	result := Result{Parts: parts, SkillDiscovery: discovery}
	result.SystemPrompt = joinParts(parts)
	result.Hash = hashParts(parts)
	return result, nil
}

func (b DefaultBuilder) selectSkills(skills []skill.Skill, query string, disclosure skill.DisclosureMode) ([]skill.Skill, int) {
	selector := b.skillSelector(disclosure)
	selected := selector.Select(skills, query)
	if disclosure != skill.DisclosureProgressive || selector.MaxSkills <= 0 {
		return selected, 0
	}
	unbounded := selector
	unbounded.MaxSkills = 0
	all := unbounded.Select(skills, query)
	if len(all) <= len(selected) {
		return selected, 0
	}
	return selected, len(all) - len(selected)
}

func (b DefaultBuilder) skillSelector(disclosure skill.DisclosureMode) skill.Selector {
	selector := b.SkillSelector
	if disclosure == skill.DisclosureProgressive && selector.MaxSkills <= 0 {
		selector.MaxSkills = defaultProgressiveSkillLimit
	}
	return selector
}

func (b DefaultBuilder) skillDiscoveryMaxBytes(disclosure skill.DisclosureMode) int {
	if disclosure != skill.DisclosureProgressive {
		return -1
	}
	if b.SkillDiscoveryMaxBytes == 0 {
		return defaultProgressiveSkillDiscoveryBytes
	}
	return b.SkillDiscoveryMaxBytes
}

// Profile tunes prompt guidance for a provider family without leaking provider
// request types into the SDK core.
type Profile string

const (
	ProfileGeneric   Profile = "generic"
	ProfileOpenAI    Profile = "openai"
	ProfileAnthropic Profile = "anthropic"
)

// WithDefault returns ProfileGeneric when p is empty.
func (p Profile) WithDefault() Profile {
	if p == "" {
		return ProfileGeneric
	}
	return p
}

func requestQuery(req Request) string {
	values := make([]string, 0, 2+maxSelectorQueryMessages)
	if value := strings.TrimSpace(req.SystemPrompt); value != "" {
		values = append(values, value)
	}
	if value := strings.TrimSpace(req.AppendSystemPrompt); value != "" {
		values = append(values, value)
	}
	messageText := recentUserText(req.Messages, maxSelectorQueryMessages)
	values = append(values, messageText...)
	return limitStringBytes(strings.Join(values, " "), maxSelectorQueryBytes)
}

func recentUserText(messages []model.Message, max int) []string {
	if max <= 0 || len(messages) == 0 {
		return nil
	}
	selected := make([]string, 0, max)
	for i := len(messages) - 1; i >= 0 && len(selected) < max; i-- {
		if messages[i].Role != model.RoleUser {
			continue
		}
		if text := strings.TrimSpace(messages[i].PlainText()); text != "" {
			selected = append(selected, text)
		}
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return selected
}

func limitStringBytes(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	var b strings.Builder
	for _, r := range value {
		if b.Len()+len(string(r)) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

const coreInstructions = `You are running inside the Memax Agent SDK.

Operate autonomously within the host application's tools, permissions, and time limits. Think in terms of durable progress: inspect before changing state, use available task or checkpoint tools to track multi-step work, delegate only when a subagent tool is available and the task is bounded, and recover from tool errors using the error content returned to you.

Do not assume direct filesystem, shell, network, or credential access. If a capability is not present as a tool, ask for it or explain the limitation.`

func formatIdentity(id identity.Identity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\n", id.Name)
	fmt.Fprintf(&b, "Role: %s\n", id.Role)
	fmt.Fprintf(&b, "Mission: %s\n", id.Mission)
	fmt.Fprintf(&b, "Tone: %s\n", id.Tone)
	fmt.Fprintf(&b, "Autonomy: %s", id.Autonomy)
	if len(id.Constraints) > 0 {
		b.WriteString("\nConstraints:")
		for _, constraint := range id.Constraints {
			constraint = strings.TrimSpace(constraint)
			if constraint != "" {
				fmt.Fprintf(&b, "\n- %s", constraint)
			}
		}
	}
	return b.String()
}

func formatToolGuidance(tools []model.ToolSpec) string {
	var b strings.Builder
	b.WriteString("Use tools when they materially improve correctness or progress. Respect tool descriptions, schemas, and safety flags. Read-only and concurrency-safe tools are good candidates for parallel investigation. Destructive tools require stronger justification and may be denied by host policy.")
	b.WriteString("\nAvailable tool count: ")
	fmt.Fprintf(&b, "%d", len(tools))
	return b.String()
}

func formatProfile(profile Profile) string {
	switch profile {
	case ProfileOpenAI:
		return "Provider profile: use available function tools with complete JSON arguments when tool use is needed. Prefer tool calls over describing an action that a provided tool can perform."
	case ProfileAnthropic:
		return "Provider profile: use available tool-use blocks with complete JSON inputs when tool use is needed. Keep text concise around tool calls so tool results can drive the next turn."
	default:
		return ""
	}
}

func formatOutputContract(schema map[string]any) string {
	var b strings.Builder
	b.WriteString("Final answer contract: return only valid JSON that satisfies this JSON Schema. Do not wrap the JSON in markdown or add explanatory text.")
	if data, err := json.MarshalIndent(schema, "", "  "); err == nil {
		b.WriteString("\nSchema:\n")
		b.Write(data)
	}
	return b.String()
}

func formatSkills(skills []skill.Skill) string {
	var b strings.Builder
	b.WriteString("Relevant skills for this run:")
	for _, item := range skills {
		fmt.Fprintf(&b, "\n\n## %s", item.Name)
		if item.Description != "" {
			fmt.Fprintf(&b, "\nDescription: %s", item.Description)
		}
		if item.WhenToUse != "" {
			fmt.Fprintf(&b, "\nUse when: %s", item.WhenToUse)
		}
		if len(item.Tags) > 0 {
			fmt.Fprintf(&b, "\nTags: %s", strings.Join(item.Tags, ", "))
		}
		if item.Content != "" {
			fmt.Fprintf(&b, "\n%s", item.Content)
		}
	}
	return b.String()
}

func skillNames(skills []skill.Skill) []string {
	names := make([]string, 0, len(skills))
	for _, item := range skills {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func countNamedSkills(skills []skill.Skill) int {
	count := 0
	for _, item := range skills {
		if strings.TrimSpace(item.Name) != "" {
			count++
		}
	}
	return count
}

func formatSkillDiscovery(skills []skill.Skill, resourcesAvailable bool, maxBytes int, priorOmitted int) string {
	header := fmt.Sprintf("Available skill metadata for this run. Skill bodies are not in this prompt. If a skill is relevant, call the `%s` tool with its exact name before relying on its instructions. Load only the skills needed for the current task.", skill.LoadToolName)
	var entries []string
	for _, item := range skills {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		entries = append(entries, formatSkillDiscoveryEntry(item, resourcesAvailable))
	}
	return joinSkillDiscoveryEntries(header, entries, maxBytes, priorOmitted)
}

func formatSkillDiscoveryEntry(item skill.Skill, resourcesAvailable bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n- %s", item.Name)
	if item.Description != "" {
		fmt.Fprintf(&b, ": %s", item.Description)
	}
	if item.WhenToUse != "" {
		fmt.Fprintf(&b, "\n  Use when: %s", item.WhenToUse)
	}
	if len(item.Tags) > 0 {
		fmt.Fprintf(&b, "\n  Tags: %s", strings.Join(item.Tags, ", "))
	}
	if resourcesAvailable && len(item.Resources) > 0 {
		fmt.Fprintf(&b, "\n  Resources: call `%s` with skill_name %q and the resource name or path when supporting material is needed.", skill.ResourceToolName, item.Name)
		formatResourceRefs(&b, item.Resources, "  ")
	}
	return b.String()
}

func joinSkillDiscoveryEntries(header string, entries []string, maxBytes int, priorOmitted int) string {
	if len(entries) == 0 {
		return ""
	}
	if maxBytes < 0 {
		content := header + strings.Join(entries, "")
		if priorOmitted > 0 {
			content += skillDiscoveryOmissionNote(priorOmitted)
		}
		return content
	}
	var b strings.Builder
	omitted := priorOmitted
	b.WriteString(header)
	for i, entry := range entries {
		if b.Len()+len(entry) > maxBytes {
			omitted += len(entries) - i
			break
		}
		b.WriteString(entry)
	}
	if omitted > 0 {
		note := skillDiscoveryOmissionNote(omitted)
		if b.Len()+len(note) <= maxBytes {
			b.WriteString(note)
		}
	}
	if b.String() == header {
		return ""
	}
	return b.String()
}

func skillDiscoveryOmissionNote(count int) string {
	return fmt.Sprintf("\n\n%d additional skill metadata entries were omitted because the skill discovery budget was reached. Narrow the request or use host-provided skill search when more catalog coverage is needed.", count)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatResourceRefs(b *strings.Builder, refs []skill.ResourceRef, prefix string) {
	for _, ref := range refs {
		fmt.Fprintf(b, "\n%s- %s", prefix, firstNonEmptyString(ref.Name, ref.Path))
		if ref.Description != "" {
			fmt.Fprintf(b, ": %s", ref.Description)
		}
		if ref.Path != "" && ref.Path != ref.Name {
			fmt.Fprintf(b, " (path: %s)", ref.Path)
		}
		if ref.MIMEType != "" {
			fmt.Fprintf(b, " [%s]", ref.MIMEType)
		}
		if ref.Bytes > 0 {
			fmt.Fprintf(b, " [%d bytes]", ref.Bytes)
		}
		if len(ref.Tags) > 0 {
			fmt.Fprintf(b, "\n%s  Tags: %s", prefix, strings.Join(ref.Tags, ", "))
		}
	}
}

func formatMemories(memories []memory.Memory) string {
	var b strings.Builder
	b.WriteString("Durable host context for this run:")
	for _, item := range memories {
		name := strings.TrimSpace(item.Name)
		scope := strings.TrimSpace(string(item.Scope))
		if name == "" {
			name = "memory"
		}
		if scope == "" {
			scope = string(memory.ScopeCustom)
		}
		fmt.Fprintf(&b, "\n\n## %s (%s)", name, scope)
		if item.Description != "" {
			fmt.Fprintf(&b, "\nDescription: %s", item.Description)
		}
		if len(item.Tags) > 0 {
			fmt.Fprintf(&b, "\nTags: %s", strings.Join(item.Tags, ", "))
		}
		if item.Content != "" {
			fmt.Fprintf(&b, "\n%s", item.Content)
		}
	}
	return b.String()
}

func formatPlan(plan planner.Plan) string {
	var b strings.Builder
	b.WriteString("Host-provided plan for this run:")
	if value := strings.TrimSpace(plan.Goal); value != "" {
		fmt.Fprintf(&b, "\nGoal: %s", value)
	}
	if plan.State != "" {
		fmt.Fprintf(&b, "\nState: %s", plan.State)
	}
	if constraints := nonEmptyStrings(plan.Constraints); len(constraints) > 0 {
		b.WriteString("\nConstraints:")
		for _, constraint := range constraints {
			fmt.Fprintf(&b, "\n- %s", constraint)
		}
	}
	if len(plan.Steps) > 0 {
		b.WriteString("\nSteps:")
		for _, step := range plan.Steps {
			title := strings.TrimSpace(step.Title)
			if title == "" {
				title = strings.TrimSpace(step.ID)
			}
			if title == "" {
				continue
			}
			status := step.Status
			if status == "" {
				status = planner.StatusPending
			}
			if step.ID != "" {
				fmt.Fprintf(&b, "\n- [%s] %s: %s", status, step.ID, title)
			} else {
				fmt.Fprintf(&b, "\n- [%s] %s", status, title)
			}
			if notes := strings.TrimSpace(step.Notes); notes != "" {
				fmt.Fprintf(&b, "\n  Notes: %s", notes)
			}
			if hints := nonEmptyStrings(step.ToolHints); len(hints) > 0 {
				fmt.Fprintf(&b, "\n  Tool hints: %s", strings.Join(hints, ", "))
			}
			if verification := nonEmptyStrings(step.VerificationHints); len(verification) > 0 {
				fmt.Fprintf(&b, "\n  Verification hints: %s", strings.Join(verification, "; "))
			}
			if evidence := nonEmptyStrings(step.Evidence); len(evidence) > 0 {
				fmt.Fprintf(&b, "\n  Evidence: %s", strings.Join(evidence, "; "))
			}
		}
	}
	return b.String()
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func joinParts(parts []Part) string {
	contents := make([]string, 0, len(parts))
	for _, part := range parts {
		content := strings.TrimSpace(part.Content)
		if content != "" {
			contents = append(contents, content)
		}
	}
	return strings.Join(contents, "\n\n")
}

func hashParts(parts []Part) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part.Name))
		hash.Write([]byte{0})
		hash.Write([]byte(part.Content))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
