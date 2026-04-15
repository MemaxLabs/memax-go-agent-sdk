package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type stream struct {
	body  io.ReadCloser
	scan  *bufio.Scanner
	calls map[int]*functionCall
}

func newStream(body io.ReadCloser) *stream {
	scan := bufio.NewScanner(body)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &stream{
		body:  body,
		scan:  scan,
		calls: make(map[int]*functionCall),
	}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	for s.scan.Scan() {
		line := s.scan.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		event, err := s.handleData([]byte(data))
		if err != nil {
			return model.StreamEvent{}, err
		}
		if event.Kind != "" {
			return event, nil
		}
	}
	if err := s.scan.Err(); err != nil {
		return model.StreamEvent{}, fmt.Errorf("openai: read stream: %w", err)
	}
	return model.StreamEvent{}, model.ErrEndOfStream
}

func (s *stream) Close() error {
	return s.body.Close()
}

func (s *stream) handleData(data []byte) (model.StreamEvent, error) {
	var envelope struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		OutputIndex int             `json:"output_index"`
		Arguments   string          `json:"arguments"`
		Item        json.RawMessage `json:"item"`
		Error       *apiError       `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return model.StreamEvent{}, fmt.Errorf("openai: decode stream event: %w", err)
	}

	switch envelope.Type {
	case "response.output_text.delta":
		return model.StreamEvent{Kind: model.StreamText, Text: envelope.Delta}, nil
	case "response.output_item.added":
		call, ok, err := decodeFunctionCall(envelope.Item)
		if err != nil || !ok {
			return model.StreamEvent{}, err
		}
		s.calls[envelope.OutputIndex] = call
	case "response.function_call_arguments.delta":
		call := s.calls[envelope.OutputIndex]
		if call != nil {
			call.Arguments += envelope.Delta
		}
	case "response.function_call_arguments.done":
		call := s.calls[envelope.OutputIndex]
		if call != nil {
			call.Arguments = envelope.Arguments
		}
	case "response.output_item.done":
		call, ok, err := decodeFunctionCall(envelope.Item)
		if err != nil || !ok {
			return model.StreamEvent{}, err
		}
		if call.Arguments == "" && s.calls[envelope.OutputIndex] != nil {
			call.Arguments = s.calls[envelope.OutputIndex].Arguments
		}
		return model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    firstNonEmpty(call.CallID, call.ID),
				Name:  call.Name,
				Input: json.RawMessage(call.Arguments),
			},
		}, nil
	case "response.completed":
		return model.StreamEvent{}, model.ErrEndOfStream
	case "error", "response.failed":
		if envelope.Error != nil {
			return model.StreamEvent{}, envelope.Error
		}
		return model.StreamEvent{}, errors.New("openai: stream failed")
	}
	return model.StreamEvent{}, nil
}

type functionCall struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func decodeFunctionCall(data json.RawMessage) (*functionCall, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	var call functionCall
	if err := json.Unmarshal(data, &call); err != nil {
		return nil, false, fmt.Errorf("openai: decode function call: %w", err)
	}
	if call.Type != "function_call" {
		return nil, false, nil
	}
	return &call, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
