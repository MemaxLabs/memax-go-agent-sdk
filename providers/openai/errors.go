package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
