package tool

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type SelectRequest struct {
	Messages []model.Message
}

type Selector interface {
	Select(context.Context, *Registry, SelectRequest) ([]model.ToolSpec, error)
}

type SelectorFunc func(context.Context, *Registry, SelectRequest) ([]model.ToolSpec, error)

func (f SelectorFunc) Select(ctx context.Context, registry *Registry, req SelectRequest) ([]model.ToolSpec, error) {
	return f(ctx, registry, req)
}

type SearchSelector struct {
	MaxTools int
}

func (s SearchSelector) Select(_ context.Context, registry *Registry, req SelectRequest) ([]model.ToolSpec, error) {
	if registry == nil {
		return nil, nil
	}
	return SelectSpecs(registry.Specs(), QueryFromMessages(req.Messages), s.MaxTools), nil
}

func SelectSpecs(specs []model.ToolSpec, query string, maxTools int) []model.ToolSpec {
	if len(specs) == 0 {
		return nil
	}

	scores := scoreSpecs(specs, query)
	selected := make([]scoredSpec, 0, len(scores))
	for _, scored := range scores {
		spec := scored.Spec
		switch {
		case spec.AlwaysLoad:
			selected = append(selected, scored)
		case scored.Score > 1:
			selected = append(selected, scored)
		case !spec.ShouldDefer:
			selected = append(selected, scored)
		}
	}

	sort.SliceStable(selected, func(i int, j int) bool {
		left := selected[i]
		right := selected[j]
		if left.Spec.AlwaysLoad != right.Spec.AlwaysLoad {
			return left.Spec.AlwaysLoad
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		return left.Index < right.Index
	})

	if maxTools > 0 {
		selected = limitPreservingAlwaysLoad(selected, maxTools)
	}

	out := make([]model.ToolSpec, 0, len(selected))
	for _, scored := range selected {
		out = append(out, scored.Spec)
	}
	return out
}

func SearchSpecs(specs []model.ToolSpec, query string, limit int) []model.ToolSpec {
	if len(specs) == 0 {
		return nil
	}
	scored := scoreSpecs(specs, query)
	filtered := make([]scoredSpec, 0, len(scored))
	for _, item := range scored {
		if item.Score > 0 {
			filtered = append(filtered, item)
		}
	}
	sort.SliceStable(filtered, func(i int, j int) bool {
		if filtered[i].Score != filtered[j].Score {
			return filtered[i].Score > filtered[j].Score
		}
		return filtered[i].Index < filtered[j].Index
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	out := make([]model.ToolSpec, 0, len(filtered))
	for _, item := range filtered {
		out = append(out, item.Spec)
	}
	return out
}

func QueryFromMessages(messages []model.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		if msg.ToolResult != nil {
			b.WriteByte(' ')
			b.WriteString(msg.ToolResult.Name)
			b.WriteByte(' ')
			b.WriteString(msg.ToolResult.Content)
			continue
		}
		text := msg.PlainText()
		if text == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(text)
	}
	return b.String()
}

type scoredSpec struct {
	Spec  model.ToolSpec
	Score int
	Index int
}

func scoreSpecs(specs []model.ToolSpec, query string) []scoredSpec {
	tokens := tokenize(query)
	out := make([]scoredSpec, 0, len(specs))
	for i, spec := range specs {
		out = append(out, scoredSpec{
			Spec:  spec,
			Score: scoreSpec(spec, query, tokens),
			Index: i,
		})
	}
	return out
}

func scoreSpec(spec model.ToolSpec, query string, tokens []string) int {
	if len(tokens) == 0 {
		return 0
	}
	name := strings.ToLower(spec.Name)
	text := strings.ToLower(strings.Join([]string{
		spec.Name,
		spec.Description,
		spec.SearchHint,
	}, " "))
	score := 0
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(name, token) {
			score += 3
		}
		if strings.Contains(text, token) {
			score++
		}
	}
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))
	if normalizedQuery != "" && strings.Contains(text, normalizedQuery) {
		score += 5
	}
	return score
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}

func limitPreservingAlwaysLoad(items []scoredSpec, maxTools int) []scoredSpec {
	if len(items) <= maxTools {
		return items
	}
	always := make([]scoredSpec, 0, len(items))
	rest := make([]scoredSpec, 0, len(items))
	for _, item := range items {
		if item.Spec.AlwaysLoad {
			always = append(always, item)
			continue
		}
		rest = append(rest, item)
	}
	if len(always) >= maxTools {
		return always
	}
	remaining := maxTools - len(always)
	if len(rest) > remaining {
		rest = rest[:remaining]
	}
	out := make([]scoredSpec, 0, len(always)+len(rest))
	out = append(out, always...)
	out = append(out, rest...)
	return out
}
