package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultHTTPMaxBytes = 4 * 1024 * 1024

// HTTPSource loads skills from a JSON HTTP endpoint.
//
// The endpoint may return either a raw []Skill JSON array or an object with a
// "skills" array field. Headers are copied onto the request so callers can
// provide authorization or tenant routing values.
type HTTPSource struct {
	URL      string
	Client   *http.Client
	Header   http.Header
	MaxBytes int64
}

// Skills fetches and decodes skills from the configured endpoint.
func (s HTTPSource) Skills(ctx context.Context) ([]Skill, error) {
	if s.URL == "" {
		return nil, fmt.Errorf("skill: HTTPSource URL is required")
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	for key, values := range s.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("skill: HTTPSource status %d: %s", resp.StatusCode, string(data))
	}

	limit := s.MaxBytes
	if limit <= 0 {
		limit = defaultHTTPMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("skill: HTTPSource response exceeds %d bytes", limit)
	}

	skills, err := decodeHTTPSkills(data)
	if err != nil {
		return nil, err
	}
	for i := range skills {
		if skills[i].Source == "" {
			skills[i].Source = "http"
		}
	}
	return cloneSkills(skills), nil
}

func decodeHTTPSkills(data []byte) ([]Skill, error) {
	var direct []Skill
	if err := json.Unmarshal(data, &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Skills []Skill `json:"skills"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Skills, nil
}
