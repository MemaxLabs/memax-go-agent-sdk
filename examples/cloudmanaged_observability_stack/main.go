package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/remote"
	cloudsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	_ "modernc.org/sqlite"
)

const examplePrompt = "Summarize the observed managed worker path."

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample shows the managed-runtime observability path in one runnable
// process: tenant denial audit, low-cardinality metrics, remote claim
// discovery, worker execution, and durable run lifecycle all share the same
// cloudmanaged stack seams. Production deployments split the ClaimHandler and
// remote.Watch sides into separate processes, as shown by
// examples/cloudmanaged_remote_stack.
func runExample(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "memax-cloudmanaged-observability-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "runs.db")
	meter := &exampleMetricMeter{}
	auditSink := &cloudmanaged.MemorySink{}
	modelClient := &delayedTextModel{text: "observed remote worker completed", delay: 20 * time.Millisecond}

	serverStack, serverDB, err := buildStack(ctx, dbPath, modelClient, meter, auditSink)
	if err != nil {
		return err
	}
	defer serverDB.Close()

	for range serverStack.QueryAsync(ctx, "start without a tenant to prove denial observability", tenant.Scope{}) {
	}

	record, err := serverStack.EnqueueRun(ctx, examplePrompt, exampleScope())
	if err != nil {
		return err
	}

	handler, err := remote.ClaimHandler(serverStack.RunStore())
	if err != nil {
		return err
	}
	var (
		serverMu   sync.Mutex
		serverGETs int
	)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		serverMu.Lock()
		serverGETs++
		serverMu.Unlock()
		handler.ServeHTTP(rw, req)
	}))
	defer server.Close()

	workerStack, workerDB, err := buildStack(ctx, dbPath, modelClient, meter, auditSink)
	if err != nil {
		return err
	}
	defer workerDB.Close()

	pool, err := remote.NewHTTPPool(server.URL)
	if err != nil {
		return err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- remote.Watch(watchCtx, workerStack, pool, cloudmanaged.WorkerOptions{
			ID: "observability-worker-1",
			// Aggressive for the bounded demo; production workers should use
			// an interval measured in seconds.
			HeartbeatInterval: time.Millisecond,
		}, time.Millisecond)
	}()

	final, err := waitForRun(workerStack, record.ID, func(r cloudmanaged.RunRecord) bool { return r.Terminal() })
	cancel()
	if watchErr := <-watchDone; watchErr != nil && !errors.Is(watchErr, context.Canceled) {
		return watchErr
	}
	if err != nil {
		return err
	}

	for _, record := range auditSink.Records() {
		switch record.Kind {
		case memaxagent.EventTenantDenied:
			if record.Tenant != nil {
				fmt.Fprintf(w, "audit tenant denied: boundary=%s reason=%q\n", record.Tenant.Boundary, record.Tenant.Reason)
			}
		case memaxagent.EventRunStateChanged:
			if record.Run != nil && record.Run.RunID == final.ID {
				fmt.Fprintf(w, "audit run state: %s worker=%s\n", record.Run.Status, record.Run.WorkerID)
			}
		case memaxagent.EventResult:
			fmt.Fprintf(w, "audit result: %s\n", record.Result)
		}
	}

	serverMu.Lock()
	gets := serverGETs
	serverMu.Unlock()
	fmt.Fprintf(w, "server GET /claim: %d\n", gets)
	fmt.Fprintf(w, "worker: %s\n", final.WorkerID)
	fmt.Fprintf(w, "run: %s %s\n", final.ID, final.Status)
	fmt.Fprintf(w, "session: %s\n", final.SessionID)
	for _, line := range meter.Lines() {
		if includeExampleMetric(line) {
			fmt.Fprintf(w, "%s\n", line)
		}
	}
	return nil
}

func includeExampleMetric(line string) bool {
	if !strings.Contains(line, "memax.cloudmanaged.") {
		return false
	}
	return !strings.Contains(line, "memax.cloudmanaged.worker.heartbeat_errors")
}

func buildStack(ctx context.Context, dbPath string, client model.Client, meter telemetry.Meter, auditSink cloudmanaged.AuditSink) (cloudmanaged.Stack, *sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return cloudmanaged.Stack{}, nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return cloudmanaged.Stack{}, nil, fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return cloudmanaged.Stack{}, nil, fmt.Errorf("configure sqlite WAL mode: %w", err)
	}
	store, err := cloudsqlitestore.New(ctx, db)
	if err != nil {
		db.Close()
		return cloudmanaged.Stack{}, nil, err
	}
	config, err := cloudmanaged.PresetManagedWorker.Config()
	if err != nil {
		db.Close()
		return cloudmanaged.Stack{}, nil, err
	}
	config.Base.Model = client
	config.Base.Meter = meter
	config.Audit.Sink = auditSink
	config.QuotaStore = store
	config.RunStore = store
	config.Policies.Quota.MaxModelRequests = 4
	config.Policies.Quota.MaxToolUses = 4
	stack, err := cloudmanaged.New(config)
	if err != nil {
		db.Close()
		return cloudmanaged.Stack{}, nil, err
	}
	return stack, db, nil
}

func exampleScope() tenant.Scope {
	return tenant.Scope{
		ID:        "tenant-1",
		SubjectID: "user-1",
		Attributes: map[string]string{
			"plan": "managed-observability",
		},
	}
}

func waitForRun(stack cloudmanaged.Stack, id string, done func(cloudmanaged.RunRecord) bool) (cloudmanaged.RunRecord, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, err := stack.GetRun(context.Background(), id)
		if err != nil {
			return cloudmanaged.RunRecord{}, err
		}
		if done(record) {
			return record, nil
		}
		time.Sleep(time.Millisecond)
	}
	return cloudmanaged.RunRecord{}, fmt.Errorf("timed out waiting for run %q", id)
}

type delayedTextModel struct {
	text  string
	delay time.Duration
}

func (m *delayedTextModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
	return &delayedTextStream{text: m.text, delay: m.delay}, nil
}

type delayedTextStream struct {
	text  string
	delay time.Duration
	done  bool
}

func (s *delayedTextStream) Recv() (model.StreamEvent, error) {
	if s.done {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	s.done = true
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return model.StreamEvent{Kind: model.StreamText, Text: s.text}, nil
}

func (s *delayedTextStream) Close() error {
	return nil
}

type exampleMetricMeter struct {
	mu      sync.Mutex
	metrics []exampleMetric
	nextSeq int
}

type exampleMetric struct {
	seq   int
	kind  string
	name  string
	value string
	attrs []telemetry.Attribute
}

func (m *exampleMetricMeter) Add(_ context.Context, name string, value int64, attrs ...telemetry.Attribute) {
	m.record("counter", name, fmt.Sprintf("%d", value), attrs...)
}

func (m *exampleMetricMeter) Record(_ context.Context, name string, value float64, attrs ...telemetry.Attribute) {
	m.record("record", name, formatMetricValue(value), attrs...)
}

func (m *exampleMetricMeter) record(kind, name, value string, attrs ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSeq++
	m.metrics = append(m.metrics, exampleMetric{
		seq:   m.nextSeq,
		kind:  kind,
		name:  name,
		value: value,
		attrs: append([]telemetry.Attribute(nil), attrs...),
	})
}

func (m *exampleMetricMeter) Lines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	metrics := append([]exampleMetric(nil), m.metrics...)
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].name != metrics[j].name {
			return metrics[i].name < metrics[j].name
		}
		iAttrs := formatMetricAttrs(metrics[i].attrs)
		jAttrs := formatMetricAttrs(metrics[j].attrs)
		if iAttrs != jAttrs {
			return iAttrs < jAttrs
		}
		return metrics[i].seq < metrics[j].seq
	})
	lines := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		lines = append(lines, fmt.Sprintf("metric %s: %s=%s%s", metric.kind, metric.name, metric.value, formatMetricAttrs(metric.attrs)))
	}
	return lines
}

func formatMetricValue(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%.3f", value)
}

func formatMetricAttrs(attrs []telemetry.Attribute) string {
	if len(attrs) == 0 {
		return ""
	}
	copied := append([]telemetry.Attribute(nil), attrs...)
	sort.Slice(copied, func(i, j int) bool {
		return copied[i].Key < copied[j].Key
	})
	out := ""
	for _, attr := range copied {
		out += fmt.Sprintf(" %s=%v", attr.Key, attr.Value)
	}
	return out
}
