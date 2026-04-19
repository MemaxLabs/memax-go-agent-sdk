// Package remote provides host-owned remote worker helpers for cloudmanaged
// durable runs.
//
// The package intentionally keeps the trust model narrow: workers are expected
// to run with the same tenant-validator configuration as the enqueueing side,
// and ExecuteRun remains the authoritative claim-and-run boundary. Remote
// coordination helpers only discover candidate queued runs; they do not mint
// delegation tokens or bypass the existing tenant seam.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
)

var (
	// ErrNoRunAvailable reports that the remote claim source currently has no
	// queued run ready for this worker to attempt.
	ErrNoRunAvailable = errors.New("cloudmanaged remote worker: no queued run available")
)

// WorkerPool yields candidate queued runs for remote workers to attempt.
//
// Claim is intentionally discovery-only: ExecuteRun remains the atomic claim
// boundary, so implementations may return a run that another worker wins before
// this worker executes it.
type WorkerPool interface {
	Claim(context.Context) (cloudmanaged.RunRecord, error)
}

// Executor runs one durable managed run by ID.
type Executor interface {
	ExecuteRun(context.Context, string, cloudmanaged.WorkerOptions) (cloudmanaged.RunRecord, error)
}

// HTTPPool is a reference WorkerPool that polls one HTTP endpoint for the next
// candidate run. The endpoint returns either 204 No Content when no queued run
// is available or 200 OK with a JSON-encoded cloudmanaged.RunRecord.
type HTTPPool struct {
	claimURL    string
	client      *http.Client
	bearerToken string
	headers     http.Header
}

// HTTPPoolOption configures one HTTPPool.
type HTTPPoolOption func(*HTTPPool) error

// WithHTTPClient overrides the HTTP client used by HTTPPool. Nil is rejected.
func WithHTTPClient(client *http.Client) HTTPPoolOption {
	return func(pool *HTTPPool) error {
		if client == nil {
			return fmt.Errorf("remote HTTP client is nil")
		}
		pool.client = client
		return nil
	}
}

// WithBearerToken attaches one bearer token to claim requests.
func WithBearerToken(token string) HTTPPoolOption {
	return func(pool *HTTPPool) error {
		pool.bearerToken = strings.TrimSpace(token)
		return nil
	}
}

// WithHeader appends one HTTP header to claim requests.
func WithHeader(key, value string) HTTPPoolOption {
	return func(pool *HTTPPool) error {
		key = textTrim(key)
		if key == "" {
			return fmt.Errorf("remote HTTP header key is required")
		}
		pool.headers.Add(key, value)
		return nil
	}
}

// NewHTTPPool constructs a reference HTTP-backed worker-claim source.
func NewHTTPPool(claimURL string, options ...HTTPPoolOption) (*HTTPPool, error) {
	if textTrim(claimURL) == "" {
		return nil, fmt.Errorf("remote claim URL is required")
	}
	pool := &HTTPPool{
		claimURL: textTrim(claimURL),
		client:   http.DefaultClient,
		headers:  make(http.Header),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(pool); err != nil {
			return nil, err
		}
	}
	return pool, nil
}

// Claim implements WorkerPool.
func (p *HTTPPool) Claim(ctx context.Context) (cloudmanaged.RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.claimURL, nil)
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("build remote claim request: %w", err)
	}
	if p.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearerToken)
	}
	for key, values := range p.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("perform remote claim request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return cloudmanaged.RunRecord{}, ErrNoRunAvailable
	case http.StatusOK:
		var record cloudmanaged.RunRecord
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&record); err != nil {
			return cloudmanaged.RunRecord{}, fmt.Errorf("decode remote claim response: %w", err)
		}
		if record.ID == "" {
			return cloudmanaged.RunRecord{}, fmt.Errorf("decode remote claim response: missing run id")
		}
		return record, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return cloudmanaged.RunRecord{}, fmt.Errorf("remote claim request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

// RunOnce asks pool for one candidate run and, when present, executes it
// through executor. It returns executed=false when no run is available or when
// another worker wins the claim race before ExecuteRun starts.
func RunOnce(ctx context.Context, executor Executor, pool WorkerPool, options cloudmanaged.WorkerOptions) (cloudmanaged.RunRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return cloudmanaged.RunRecord{}, false, err
	}
	if executor == nil {
		return cloudmanaged.RunRecord{}, false, fmt.Errorf("remote executor is required")
	}
	if pool == nil {
		return cloudmanaged.RunRecord{}, false, fmt.Errorf("remote worker pool is required")
	}
	record, err := pool.Claim(ctx)
	if err != nil {
		if errors.Is(err, ErrNoRunAvailable) {
			return cloudmanaged.RunRecord{}, false, nil
		}
		return cloudmanaged.RunRecord{}, false, err
	}
	final, err := executor.ExecuteRun(ctx, record.ID, options)
	if errors.Is(err, cloudmanaged.ErrRunNotQueued) {
		return record, false, nil
	}
	if err != nil {
		return final, true, err
	}
	return final, true, nil
}

// Watch continuously polls pool for queued work and executes each candidate
// through executor until ctx is canceled.
func Watch(ctx context.Context, executor Executor, pool WorkerPool, options cloudmanaged.WorkerOptions, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("remote worker interval must be positive")
	}
	run := func() error {
		_, _, err := RunOnce(ctx, executor, pool, options)
		return err
	}
	if err := run(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := run(); err != nil {
				return err
			}
		}
	}
}

func textTrim(value string) string {
	return strings.TrimSpace(value)
}
