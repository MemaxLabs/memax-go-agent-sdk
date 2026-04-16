package workspace

import (
	"fmt"
	"strconv"
	"strings"
)

type unifiedFilePatch struct {
	oldPath string
	newPath string
	hunks   []unifiedHunk
}

type unifiedHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []unifiedLine
}

type unifiedLine struct {
	kind byte
	text string
}

func unifiedDiffOperations(diff string, files map[string]string) ([]PatchOperation, error) {
	patches, err := parseUnifiedDiff(diff)
	if err != nil {
		return nil, err
	}
	if len(patches) == 0 {
		return nil, fmt.Errorf("workspace: unified diff contains no file patches")
	}
	ops := make([]PatchOperation, 0, len(patches))
	for _, patch := range patches {
		pathName := unifiedPatchPath(patch)
		if invalidPath(pathName) {
			return nil, fmt.Errorf("workspace: invalid file path: %s", pathName)
		}
		before, exists := files[pathName]
		if patch.oldPath == "/dev/null" {
			if exists {
				return nil, fmt.Errorf("workspace: unified diff add failed for %s: file already exists", pathName)
			}
			before = ""
		} else if !exists {
			return nil, fmt.Errorf("workspace: unified diff failed for %s: file does not exist", pathName)
		}
		after, err := applyUnifiedFilePatch(pathName, before, exists, patch)
		if err != nil {
			return nil, err
		}
		op := PatchOperation{Path: pathName}
		if patch.oldPath != "/dev/null" {
			oldContent := before
			op.OldContent = &oldContent
		}
		if patch.newPath != "/dev/null" {
			newContent := after
			op.NewContent = &newContent
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func parseUnifiedDiff(diff string) ([]unifiedFilePatch, error) {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	raw := strings.Split(diff, "\n")
	var patches []unifiedFilePatch
	for i := 0; i < len(raw); {
		line := raw[i]
		if !strings.HasPrefix(line, "--- ") {
			i++
			continue
		}
		oldPath := parseUnifiedPath(line[4:])
		i++
		if i >= len(raw) || !strings.HasPrefix(raw[i], "+++ ") {
			return nil, fmt.Errorf("workspace: unified diff expected +++ after --- %s", oldPath)
		}
		newPath := parseUnifiedPath(raw[i][4:])
		i++
		patch := unifiedFilePatch{oldPath: oldPath, newPath: newPath}
		for i < len(raw) {
			line = raw[i]
			if strings.HasPrefix(line, "--- ") {
				break
			}
			if line == "" && i == len(raw)-1 {
				i++
				break
			}
			if !strings.HasPrefix(line, "@@ ") {
				i++
				continue
			}
			hunk, next, err := parseUnifiedHunk(raw, i)
			if err != nil {
				return nil, err
			}
			patch.hunks = append(patch.hunks, hunk)
			i = next
		}
		if len(patch.hunks) == 0 {
			return nil, fmt.Errorf("workspace: unified diff for %s has no hunks", unifiedPatchPath(patch))
		}
		patches = append(patches, patch)
	}
	return patches, nil
}

func parseUnifiedHunk(lines []string, start int) (unifiedHunk, int, error) {
	header := lines[start]
	end := strings.Index(header[3:], "@@")
	if end < 0 {
		return unifiedHunk{}, start, fmt.Errorf("workspace: invalid unified hunk header: %s", header)
	}
	headerFields := strings.Fields(header[3 : 3+end])
	if len(headerFields) < 2 {
		return unifiedHunk{}, start, fmt.Errorf("workspace: invalid unified hunk header: %s", header)
	}
	oldStart, oldCount, err := parseUnifiedRange(headerFields[0], '-')
	if err != nil {
		return unifiedHunk{}, start, err
	}
	newStart, newCount, err := parseUnifiedRange(headerFields[1], '+')
	if err != nil {
		return unifiedHunk{}, start, err
	}
	hunk := unifiedHunk{
		oldStart: oldStart,
		oldCount: oldCount,
		newStart: newStart,
		newCount: newCount,
	}
	i := start + 1
	oldSeen := 0
	newSeen := 0
	for i < len(lines) {
		if oldSeen == hunk.oldCount && newSeen == hunk.newCount {
			break
		}
		line := lines[i]
		if line == `\ No newline at end of file` {
			i++
			continue
		}
		if line == "" && i == len(lines)-1 {
			break
		}
		switch line[0] {
		case ' ', '-', '+':
			hunk.lines = append(hunk.lines, unifiedLine{kind: line[0], text: line[1:]})
			switch line[0] {
			case ' ':
				oldSeen++
				newSeen++
			case '-':
				oldSeen++
			case '+':
				newSeen++
			}
			if oldSeen > hunk.oldCount || newSeen > hunk.newCount {
				return unifiedHunk{}, start, fmt.Errorf("workspace: unified hunk has more lines than header declares")
			}
		default:
			return unifiedHunk{}, start, fmt.Errorf("workspace: invalid unified hunk line %d: %s", i+1, line)
		}
		i++
	}
	if err := validateUnifiedHunkCounts(hunk); err != nil {
		return unifiedHunk{}, start, err
	}
	return hunk, i, nil
}

func parseUnifiedRange(value string, prefix byte) (int, int, error) {
	if value == "" || value[0] != prefix {
		return 0, 0, fmt.Errorf("workspace: invalid unified range: %s", value)
	}
	body := value[1:]
	count := 1
	if idx := strings.IndexByte(body, ','); idx >= 0 {
		parsedCount, err := strconv.Atoi(body[idx+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("workspace: invalid unified range count: %s", value)
		}
		count = parsedCount
		body = body[:idx]
	}
	line, err := strconv.Atoi(body)
	if err != nil {
		return 0, 0, fmt.Errorf("workspace: invalid unified range start: %s", value)
	}
	return line, count, nil
}

func validateUnifiedHunkCounts(hunk unifiedHunk) error {
	oldCount := 0
	newCount := 0
	for _, line := range hunk.lines {
		switch line.kind {
		case ' ':
			oldCount++
			newCount++
		case '-':
			oldCount++
		case '+':
			newCount++
		}
	}
	if oldCount != hunk.oldCount || newCount != hunk.newCount {
		return fmt.Errorf("workspace: invalid unified hunk counts: header -%d +%d, body -%d +%d", hunk.oldCount, hunk.newCount, oldCount, newCount)
	}
	return nil
}

func applyUnifiedFilePatch(name string, before string, exists bool, patch unifiedFilePatch) (string, error) {
	lines, trailingNewline := splitContentLines(before)
	out := make([]string, 0, len(lines))
	pos := 0
	for _, hunk := range patch.hunks {
		target := hunk.oldStart - 1
		if hunk.oldStart == 0 {
			target = 0
		}
		if target < pos || target > len(lines) {
			return "", fmt.Errorf("workspace: unified diff failed for %s: hunk starts at line %d outside current file", name, hunk.oldStart)
		}
		out = append(out, lines[pos:target]...)
		pos = target
		for _, line := range hunk.lines {
			switch line.kind {
			case ' ':
				if pos >= len(lines) || lines[pos] != line.text {
					return "", unifiedLineMismatch(name, pos, line.text, lines)
				}
				out = append(out, line.text)
				pos++
			case '-':
				if pos >= len(lines) || lines[pos] != line.text {
					return "", unifiedLineMismatch(name, pos, line.text, lines)
				}
				pos++
			case '+':
				out = append(out, line.text)
			}
		}
	}
	out = append(out, lines[pos:]...)
	if patch.newPath == "/dev/null" {
		if len(out) != 0 {
			return "", fmt.Errorf("workspace: unified diff delete failed for %s: patch leaves %d lines", name, len(out))
		}
		return "", nil
	}
	if patch.oldPath == "/dev/null" && exists {
		return "", fmt.Errorf("workspace: unified diff add failed for %s: file already exists", name)
	}
	return joinContentLines(out, trailingNewline), nil
}

func parseUnifiedPath(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.IndexAny(value, "\t "); idx >= 0 {
		value = value[:idx]
	}
	if value == "/dev/null" {
		return value
	}
	value = strings.TrimPrefix(value, "a/")
	value = strings.TrimPrefix(value, "b/")
	return cleanPath(value)
}

func unifiedPatchPath(patch unifiedFilePatch) string {
	if patch.newPath != "" && patch.newPath != "/dev/null" {
		return patch.newPath
	}
	return patch.oldPath
}

func splitContentLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	if trailingNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), trailingNewline
}

func joinContentLines(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out
}

func unifiedLineMismatch(name string, index int, expected string, lines []string) error {
	actual := "<EOF>"
	if index < len(lines) {
		actual = lines[index]
	}
	return fmt.Errorf("workspace: unified diff failed for %s at line %d: expected %q, found %q; nearby current content: %s", name, index+1, expected, actual, lineWindow(lines, index))
}

func contentMismatch(actual string, expected string) string {
	index := firstDiffIndex(actual, expected)
	return fmt.Sprintf("first difference at byte %d, expected %d bytes, found %d bytes, nearby current content: %q", index, len(expected), len(actual), contentWindow(actual, index))
}

func firstDiffIndex(actual string, expected string) int {
	limit := len(actual)
	if len(expected) < limit {
		limit = len(expected)
	}
	for i := 0; i < limit; i++ {
		if actual[i] != expected[i] {
			return i
		}
	}
	return limit
}

func contentWindow(content string, index int) string {
	const window = 80
	if index < 0 {
		index = 0
	}
	start := index - window/2
	if start < 0 {
		start = 0
	}
	end := start + window
	if end > len(content) {
		end = len(content)
	}
	return content[start:end]
}

func lineWindow(lines []string, index int) string {
	if len(lines) == 0 {
		return "<empty>"
	}
	start := index - 2
	if start < 0 {
		start = 0
	}
	end := index + 3
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "%d:%s", i+1, lines[i])
	}
	return b.String()
}
