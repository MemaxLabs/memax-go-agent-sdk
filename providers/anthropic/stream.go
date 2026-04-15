package anthropic

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
	body         io.ReadCloser
	scan         *bufio.Scanner
	blocks       map[int]*toolUseBlock
	pendingEvent string
	pendingData  []string
}

func newStream(body io.ReadCloser) *stream {
	scan := bufio.NewScanner(body)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &stream{
		body:   body,
		scan:   scan,
		blocks: make(map[int]*toolUseBlock),
	}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	for {
		eventName, data, err := s.nextSSE()
		if err != nil {
			return model.StreamEvent{}, err
		}
		if data == "" {
			continue
		}
		event, err := s.handleData(eventName, []byte(data))
		if err != nil {
			return model.StreamEvent{}, err
		}
		if event.Kind != "" {
			return event, nil
		}
	}
}

func (s *stream) Close() error {
	return s.body.Close()
}

func (s *stream) nextSSE() (string, string, error) {
	for s.scan.Scan() {
		line := s.scan.Text()
		if line == "" {
			return s.flushSSE()
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			s.pendingEvent = value
		case "data":
			s.pendingData = append(s.pendingData, value)
		}
	}
	if err := s.scan.Err(); err != nil {
		return "", "", fmt.Errorf("anthropic: read stream: %w", err)
	}
	if len(s.pendingData) > 0 || s.pendingEvent != "" {
		return s.flushSSE()
	}
	return "", "", model.ErrEndOfStream
}

func (s *stream) flushSSE() (string, string, error) {
	eventName := s.pendingEvent
	data := strings.Join(s.pendingData, "\n")
	s.pendingEvent = ""
	s.pendingData = nil
	return eventName, data, nil
}

func (s *stream) handleData(eventName string, data []byte) (model.StreamEvent, error) {
	var envelope struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		ContentBlock json.RawMessage `json:"content_block"`
		Delta        json.RawMessage `json:"delta"`
		Error        *apiError       `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return model.StreamEvent{}, fmt.Errorf("anthropic: decode stream event: %w", err)
	}
	if envelope.Type == "" {
		envelope.Type = eventName
	}

	switch envelope.Type {
	case "content_block_start":
		block, ok, err := decodeToolUseBlock(envelope.ContentBlock)
		if err != nil || !ok {
			return model.StreamEvent{}, err
		}
		s.blocks[envelope.Index] = block
	case "content_block_delta":
		return s.handleDelta(envelope.Index, envelope.Delta)
	case "content_block_stop":
		block := s.blocks[envelope.Index]
		if block == nil {
			return model.StreamEvent{}, nil
		}
		delete(s.blocks, envelope.Index)
		return model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.input(),
			},
		}, nil
	case "message_stop":
		return model.StreamEvent{}, model.ErrEndOfStream
	case "error":
		if envelope.Error != nil {
			return model.StreamEvent{}, envelope.Error
		}
		return model.StreamEvent{}, errors.New("anthropic: stream failed")
	}
	return model.StreamEvent{}, nil
}

func (s *stream) handleDelta(index int, data json.RawMessage) (model.StreamEvent, error) {
	var delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	}
	if err := json.Unmarshal(data, &delta); err != nil {
		return model.StreamEvent{}, fmt.Errorf("anthropic: decode delta: %w", err)
	}
	switch delta.Type {
	case "text_delta":
		return model.StreamEvent{Kind: model.StreamText, Text: delta.Text}, nil
	case "input_json_delta":
		block := s.blocks[index]
		if block != nil {
			block.Input = append(block.Input, delta.PartialJSON...)
		}
	}
	return model.StreamEvent{}, nil
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (b *toolUseBlock) input() json.RawMessage {
	if len(b.Input) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), b.Input...)
}

func decodeToolUseBlock(data json.RawMessage) (*toolUseBlock, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	var block toolUseBlock
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, false, fmt.Errorf("anthropic: decode content block: %w", err)
	}
	if block.Type != "tool_use" {
		return nil, false, nil
	}
	switch string(block.Input) {
	case "", "{}":
		block.Input = nil
	default:
		block.Input = append([]byte(nil), block.Input...)
	}
	return &block, true, nil
}
