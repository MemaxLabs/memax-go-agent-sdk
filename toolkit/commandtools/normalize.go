package commandtools

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func normalizeShellCommandInput(numericFields ...string) tool.InputNormalizer {
	return tool.InputNormalizerFunc(func(_ context.Context, use model.ToolUse) (model.ToolUse, bool, error) {
		return normalizeObjectInput(use, func(fields map[string]json.RawMessage) (bool, error) {
			changed := normalizeCommandArrayField(fields, "command")
			numericChanged, err := normalizeNumericStringFields(fields, numericFields...)
			return changed || numericChanged, err
		})
	})
}

func normalizeNumericInput(numericFields ...string) tool.InputNormalizer {
	return tool.InputNormalizerFunc(func(_ context.Context, use model.ToolUse) (model.ToolUse, bool, error) {
		return normalizeObjectInput(use, func(fields map[string]json.RawMessage) (bool, error) {
			return normalizeNumericStringFields(fields, numericFields...)
		})
	})
}

func normalizeObjectInput(use model.ToolUse, normalize func(map[string]json.RawMessage) (bool, error)) (model.ToolUse, bool, error) {
	if len(bytes.TrimSpace(use.Input)) == 0 {
		return use, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(use.Input, &fields); err != nil {
		// Leave malformed or non-object payloads untouched so schema validation
		// returns the same user-facing tool error it would without a normalizer.
		return use, false, nil
	}
	changed, err := normalize(fields)
	if err != nil || !changed {
		return use, changed, err
	}
	input, err := json.Marshal(fields)
	if err != nil {
		return use, false, err
	}
	use.Input = input
	return use, true, nil
}

func normalizeCommandArrayField(fields map[string]json.RawMessage, name string) bool {
	raw := bytes.TrimSpace(fields[name])
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil || len(argv) == 0 {
		return false
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, '\x00') {
			return false
		}
	}
	argv = normalizeArgv(argv)
	if len(argv) == 0 {
		return false
	}
	command := shellQuoteJoin(argv)
	if strings.TrimSpace(command) == "" {
		return false
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return false
	}
	fields[name] = encoded
	return true
}

func normalizeNumericStringFields(fields map[string]json.RawMessage, names ...string) (bool, error) {
	changed := false
	for _, name := range names {
		raw := bytes.TrimSpace(fields[name])
		if len(raw) == 0 || raw[0] != '"' {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return changed, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}
		fields[name] = []byte(strconv.FormatInt(parsed, 10))
		changed = true
	}
	return changed, nil
}

func shellQuoteJoin(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, shellQuoteArg(arg))
	}
	return strings.Join(parts, " ")
}

// shellQuoteArg targets the POSIX sh/bash command strings used by the shell
// command tools. Hosts that need exact argv semantics should expose NewExecTool.
func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if isSafeShellArg(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func isSafeShellArg(arg string) bool {
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+:,./-", r):
		default:
			return false
		}
	}
	return true
}
