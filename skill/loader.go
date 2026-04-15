package skill

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const skillFileName = "SKILL.md"

// LoadDir loads skills from subdirectories containing SKILL.md files.
//
// Each skill file may start with a small frontmatter block delimited by ---.
// Supported keys are name, description, when, when_to_use, always_on, and tags.
func LoadDir(ctx context.Context, dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []Skill
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), skillFileName)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		parsed, err := parseSkillFile(entry.Name(), path, "local", string(data))
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

// LoadFS loads skills from subdirectories in fsys containing SKILL.md files.
//
// Use this for embedded skills, map-backed filesystems, archives, or any other
// standard fs.FS implementation.
func LoadFS(ctx context.Context, fsys fs.FS, root string) ([]Skill, error) {
	if fsys == nil {
		return nil, fmt.Errorf("skill: nil fs")
	}
	if root == "" {
		root = "."
	}
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}

	var out []Skill
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		filePath := path.Join(root, entry.Name(), skillFileName)
		data, err := fs.ReadFile(fsys, filePath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		parsed, err := parseSkillFile(entry.Name(), filePath, "fs", string(data))
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseSkillFile(defaultName string, path string, source string, data string) (Skill, error) {
	meta, body := parseFrontmatter(data)
	out := Skill{
		Name:        firstNonEmpty(meta["name"], defaultName),
		Description: meta["description"],
		WhenToUse:   firstNonEmpty(meta["when_to_use"], meta["when"]),
		Content:     strings.TrimSpace(body),
		Source:      source,
		Path:        path,
		Tags:        splitList(meta["tags"]),
	}
	if value := meta["always_on"]; value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Skill{}, fmt.Errorf("skill %q always_on: %w", out.Name, err)
		}
		out.AlwaysOn = parsed
	}
	return out, nil
}

func parseFrontmatter(data string) (map[string]string, string) {
	meta := map[string]string{}
	trimmed := strings.TrimLeft(data, "\ufeff\r\n\t ")
	if !strings.HasPrefix(trimmed, "---") {
		return meta, data
	}

	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return meta, data
	}

	var body strings.Builder
	inMeta := true
	for scanner.Scan() {
		line := scanner.Text()
		if inMeta {
			if strings.TrimSpace(line) == "---" {
				inMeta = false
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if ok {
				meta[strings.ToLower(strings.TrimSpace(key))] = trimScalar(value)
			}
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if inMeta {
		return map[string]string{}, data
	}
	return meta, body.String()
}

func trimScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return value
}

func splitList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = trimScalar(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
