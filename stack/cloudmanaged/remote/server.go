package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
)

// ReadinessState represents a remote claim-server readiness state.
type ReadinessState string

const (
	// ReadinessOK reports that the claim server can reach queued-run discovery.
	ReadinessOK ReadinessState = "ok"
	// ReadinessNotReady reports that queued-run discovery is unavailable.
	ReadinessNotReady ReadinessState = "not_ready"
)

// ReadinessReport is the JSON shape returned by ReadinessHandler.
type ReadinessReport struct {
	Status             ReadinessState `json:"status"`
	QueuedRunDiscovery bool           `json:"queued_run_discovery"`
	Error              string         `json:"error,omitempty"`
}

// ClaimHandler serves one discovery-only HTTP endpoint for the next queued run.
//
// The handler returns 204 No Content when no queued run is available and 200 OK
// with one JSON-encoded cloudmanaged.RunRecord when a candidate run exists.
// Returned records remain queued until a worker later calls ExecuteRun.
func ClaimHandler(store cloudmanaged.RunStore) (http.Handler, error) {
	queue, ok := store.(cloudmanaged.RunStoreWithNextQueued)
	if !ok {
		return nil, fmt.Errorf("cloudmanaged run store does not support queued-run discovery")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		record, err := queue.NextQueuedRun(r.Context())
		if err != nil {
			if errors.Is(err, cloudmanaged.ErrRunQueueEmpty) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(record); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}), nil
}

// CheckReadiness probes whether store can serve queued-run discovery without
// mutating run state. An empty queue is ready; any other discovery error is not.
func CheckReadiness(ctx context.Context, store cloudmanaged.RunStore) ReadinessReport {
	report := ReadinessReport{Status: ReadinessNotReady}
	queue, ok := store.(cloudmanaged.RunStoreWithNextQueued)
	if !ok {
		report.Error = "cloudmanaged run store does not support queued-run discovery"
		return report
	}
	report.QueuedRunDiscovery = true
	if _, err := queue.NextQueuedRun(ctx); err != nil && !errors.Is(err, cloudmanaged.ErrRunQueueEmpty) {
		report.Error = err.Error()
		return report
	}
	report.Status = ReadinessOK
	return report
}

// ReadinessHandler serves a GET/HEAD readiness endpoint for remote claim
// servers. It returns 200 OK when queued-run discovery succeeds or the queue is
// empty, and 503 Service Unavailable when the backing store cannot be probed.
// Unlike ClaimHandler, unsupported stores are reported at probe time so the
// endpoint can expose live misconfiguration to orchestrators.
func ReadinessHandler(store cloudmanaged.RunStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		report := CheckReadiness(r.Context(), store)
		status := http.StatusOK
		if report.Status != ReadinessOK {
			status = http.StatusServiceUnavailable
		}
		body, err := json.Marshal(report)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(append(body, '\n'))
	})
}
