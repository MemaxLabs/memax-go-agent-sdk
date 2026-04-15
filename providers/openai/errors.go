package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e *apiError) Error() string {
	if e == nil {
		return "openai: unknown API error"
	}
	if e.Code != "" {
		return fmt.Sprintf("openai: %s (%s)", e.Message, e.Code)
	}
	if e.Type != "" {
		return fmt.Sprintf("openai: %s (%s)", e.Message, e.Type)
	}
	return "openai: " + e.Message
}

func (e *apiError) Is(target error) bool {
	return target == model.ErrContextWindowExceeded && e.contextWindowExceeded()
}

func (e *apiError) contextWindowExceeded() bool {
	if e == nil {
		return false
	}
	code := strings.ToLower(e.Code)
	typ := strings.ToLower(e.Type)
	message := strings.ToLower(e.Message)
	return strings.Contains(code, "context_length") ||
		strings.Contains(code, "context_window") ||
		strings.Contains(typ, "context_length") ||
		strings.Contains(message, "context length") ||
		strings.Contains(message, "context window") ||
		strings.Contains(message, "maximum context") ||
		strings.Contains(message, "too many tokens")
}

func decodeError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("openai: status %d: read error body: %w", resp.StatusCode, err)
	}
	var payload struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != nil {
		return payload.Error
	}
	return fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(body))
}
