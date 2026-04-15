package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	if e == nil {
		return "anthropic: unknown API error"
	}
	if e.Type != "" {
		return fmt.Sprintf("anthropic: %s (%s)", e.Message, e.Type)
	}
	return "anthropic: " + e.Message
}

func (e *apiError) Is(target error) bool {
	return target == model.ErrContextWindowExceeded && e.contextWindowExceeded()
}

func (e *apiError) contextWindowExceeded() bool {
	if e == nil {
		return false
	}
	typ := strings.ToLower(e.Type)
	message := strings.ToLower(e.Message)
	return strings.Contains(typ, "context_window") ||
		strings.Contains(typ, "context_length") ||
		strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "prompt too long") ||
		strings.Contains(message, "context window") ||
		strings.Contains(message, "maximum context") ||
		strings.Contains(message, "too many tokens")
}

func decodeError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("anthropic: status %d: read error body: %w", resp.StatusCode, err)
	}
	var payload struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != nil {
		return payload.Error
	}
	return fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(body))
}
