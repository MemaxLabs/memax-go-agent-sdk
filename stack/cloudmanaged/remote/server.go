package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
)

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
