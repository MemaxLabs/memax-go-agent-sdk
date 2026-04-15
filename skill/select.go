package skill

import (
	"sort"
	"strings"
	"unicode"
)

// Selector selects a deterministic, relevant skill subset.
type Selector struct {
	MaxSkills int
}

// Select returns relevant skills for query. AlwaysOn skills are preserved even
// when MaxSkills would otherwise exclude them.
func (s Selector) Select(skills []Skill, query string) []Skill {
	if len(skills) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	tokens := tokenize(query)
	items := make([]scoredSkill, 0, len(skills))
	for i, skill := range skills {
		score := scoreSkill(skill, query, tokens)
		if skill.AlwaysOn || score > 0 || query == "" {
			items = append(items, scoredSkill{Skill: clone(skill), index: i, score: score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.AlwaysOn != right.AlwaysOn {
			return left.AlwaysOn
		}
		if left.score != right.score {
			return left.score > right.score
		}
		if left.index != right.index {
			return left.index < right.index
		}
		return left.Name < right.Name
	})
	items = limitSkills(items, s.MaxSkills)
	out := make([]Skill, len(items))
	for i, item := range items {
		out[i] = item.Skill
	}
	return out
}

type scoredSkill struct {
	Skill
	index int
	score int
}

func limitSkills(items []scoredSkill, max int) []scoredSkill {
	if max <= 0 || len(items) <= max {
		return items
	}
	var always []scoredSkill
	var rest []scoredSkill
	for _, item := range items {
		if item.AlwaysOn {
			always = append(always, item)
		} else {
			rest = append(rest, item)
		}
	}
	if len(always) >= max {
		return always
	}
	remaining := max - len(always)
	if len(rest) > remaining {
		rest = rest[:remaining]
	}
	return append(always, rest...)
}

func scoreSkill(skill Skill, query string, tokens []string) int {
	combined := strings.ToLower(strings.Join([]string{
		skill.Name,
		skill.Description,
		skill.WhenToUse,
		strings.Join(skill.Tags, " "),
		skill.Content,
	}, " "))
	score := 0
	if query != "" && strings.Contains(combined, query) {
		score += 5
	}
	name := strings.ToLower(skill.Name)
	for _, token := range tokens {
		if strings.Contains(name, token) {
			score += 3
			continue
		}
		if strings.Contains(combined, token) {
			score++
		}
	}
	return score
}

func tokenize(value string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		field = strings.ToLower(strings.TrimSpace(field))
		if len(field) <= 1 {
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
