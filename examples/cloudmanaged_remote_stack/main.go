package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/remote"
	cloudsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	_ "modernc.org/sqlite"
)

const (
	defaultPrompt = "Summarize the managed remote worker path."
	defaultAddr   = "127.0.0.1:8080"
	defaultDB     = "cloudmanaged_remote.db"
)

func main() {
	mode := flag.String("mode", "demo", "run mode: demo, server, or worker")
	addr := flag.String("addr", defaultAddr, "server listen address for -mode=server")
	claimURL := flag.String("claim-url", "http://"+defaultAddr+"/claim", "claim endpoint URL for -mode=worker")
	dbPath := flag.String("db", defaultDB, "SQLite database path shared by server and worker modes")
	flag.Parse()

	if err := run(context.Background(), os.Stdout, *mode, *addr, *claimURL, *dbPath); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, w io.Writer, mode, addr, claimURL, dbPath string) error {
	switch mode {
	case "", "demo":
		return runDemo(ctx, w)
	case "server":
		return runServer(ctx, w, addr, dbPath)
	case "worker":
		return runWorker(ctx, w, claimURL, dbPath)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

// runDemo starts both sides in-process so `go run
// ./examples/cloudmanaged_remote_stack` works without external services. The
// -mode=server and -mode=worker flags use the same wiring over a shared SQLite
// database when hosts want to split the processes.
func runDemo(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "memax-cloudmanaged-remote-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "runs.db")
	modelClient := &singleTextModel{text: "remote worker finished the managed run"}
	serverStack, serverDB, err := buildStack(ctx, dbPath, modelClient)
	if err != nil {
		return err
	}
	defer serverDB.Close()

	var events []memaxagent.Event
	var eventsMu sync.Mutex
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	record, err := serverStack.EnqueueRun(memaxagent.WithEventObserver(ctx, observer), defaultPrompt, demoScope())
	if err != nil {
		return err
	}

	handler, err := remote.ClaimHandler(serverStack.RunStore())
	if err != nil {
		return err
	}
	var serverGETs int
	var serverMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		serverMu.Lock()
		serverGETs++
		serverMu.Unlock()
		handler.ServeHTTP(rw, req)
	}))
	defer server.Close()

	workerStack, workerDB, err := buildStack(ctx, dbPath, modelClient)
	if err != nil {
		return err
	}
	defer workerDB.Close()
	pool, err := remote.NewHTTPPool(server.URL)
	if err != nil {
		return err
	}

	watchCtx, cancel := context.WithCancel(memaxagent.WithEventObserver(ctx, observer))
	defer cancel()
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- remote.Watch(watchCtx, workerStack, pool, cloudmanaged.WorkerOptions{
			ID:                "example-worker-1",
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

	eventsMu.Lock()
	captured := append([]memaxagent.Event(nil), events...)
	eventsMu.Unlock()
	for _, event := range captured {
		if event.Kind == memaxagent.EventRunStateChanged && event.Run != nil && event.Run.RunID == record.ID {
			fmt.Fprintf(w, "run state: %s\n", event.Run.Status)
		}
		if event.Kind == memaxagent.EventResult {
			fmt.Fprintf(w, "result: %s\n", event.Result)
		}
	}
	serverMu.Lock()
	gets := serverGETs
	serverMu.Unlock()
	fmt.Fprintf(w, "server GET /claim: %d\n", gets)
	fmt.Fprintf(w, "worker: %s\n", final.WorkerID)
	fmt.Fprintf(w, "run: %s %s\n", final.ID, final.Status)
	fmt.Fprintf(w, "session: %s\n", final.SessionID)
	return nil
}

func runServer(ctx context.Context, w io.Writer, addr, dbPath string) error {
	stack, db, err := buildStack(ctx, dbPath, &singleTextModel{text: "server placeholder model"})
	if err != nil {
		return err
	}
	defer db.Close()
	record, err := stack.EnqueueRun(ctx, defaultPrompt, demoScope())
	if err != nil {
		return err
	}
	handler, err := remote.ClaimHandler(stack.RunStore())
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/claim", handler)
	fmt.Fprintf(w, "queued run: %s\n", record.ID)
	fmt.Fprintf(w, "claim endpoint: http://%s/claim\n", addr)
	return http.ListenAndServe(addr, mux)
}

func runWorker(ctx context.Context, w io.Writer, claimURL, dbPath string) error {
	stack, db, err := buildStack(ctx, dbPath, &singleTextModel{text: "remote worker finished the managed run"})
	if err != nil {
		return err
	}
	defer db.Close()
	pool, err := remote.NewHTTPPool(claimURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "worker polling: %s\n", claimURL)
	return remote.Watch(ctx, stack, pool, cloudmanaged.WorkerOptions{
		ID:                "example-worker-1",
		HeartbeatInterval: time.Second,
	}, time.Second)
}

func buildStack(ctx context.Context, dbPath string, client model.Client) (cloudmanaged.Stack, *sql.DB, error) {
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

func demoScope() tenant.Scope {
	return tenant.Scope{
		ID:        "tenant-1",
		SubjectID: "user-1",
		Attributes: map[string]string{
			"plan": "managed",
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
		time.Sleep(10 * time.Millisecond)
	}
	return cloudmanaged.RunRecord{}, fmt.Errorf("timed out waiting for run %q", id)
}

type singleTextModel struct {
	mu   sync.Mutex
	text string
}

func (m *singleTextModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &singleTextStream{text: m.text}, nil
}

type singleTextStream struct {
	text string
	done bool
}

func (s *singleTextStream) Recv() (model.StreamEvent, error) {
	if s.done {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	s.done = true
	return model.StreamEvent{Kind: model.StreamText, Text: s.text}, nil
}

func (s *singleTextStream) Close() error {
	return nil
}
