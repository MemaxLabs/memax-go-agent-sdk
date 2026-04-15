package prompt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
)

const (
	maxSelectorQueryMessages = 3
	maxSelectorQueryBytes    = 4096
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
	Memories           []memory.Memory
	Skills             []skill.Skill
}

// Part is a named prompt component. Stable names make prompt snapshots and
// hashes easier to debug.
type Part struct {
	Name    string
	Content string
}

// Result is a built prompt plus prompt metadata.
type Result struct {
	SystemPrompt string
	Parts        []Part
	Hash         string
}

// DefaultBuilder is the SDK's default Memax-native prompt assembler.
type DefaultBuilder struct {
	SkillSelector  skill.Selector
	MemorySelector memory.Selector
	Profile        Profile
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
	if selected := b.MemorySelector.Select(req.Memories, requestQuery(req)); len(selected) > 0 {
		parts = append(parts, Part{Name: "memax.memories", Content: formatMemories(selected)})
	}
	if selected := b.SkillSelector.Select(req.Skills, requestQuery(req)); len(selected) > 0 {
		parts = append(parts, Part{Name: "memax.skills", Content: formatSkills(selected)})
	}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		parts = append(parts, Part{Name: "host.system", Content: strings.TrimSpace(req.SystemPrompt)})
	}
	if strings.TrimSpace(req.AppendSystemPrompt) != "" {
		parts = append(parts, Part{Name: "host.append_system", Content: strings.TrimSpace(req.AppendSystemPrompt)})
	}

	result := Result{Parts: parts}
	result.SystemPrompt = joinParts(parts)
	result.Hash = hashParts(parts)
	return result, nil
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
