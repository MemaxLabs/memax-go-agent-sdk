package cloudmanaged

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultAsyncSinkBuffer = 256

// AsyncOverflowPolicy controls how AsyncSink behaves when its buffer fills.
type AsyncOverflowPolicy string

const (
	// AsyncOverflowBlock waits for buffer space or caller cancellation.
	AsyncOverflowBlock AsyncOverflowPolicy = "block"
	// AsyncOverflowDropOldest discards the oldest queued audit record to make
	// room for the newest one.
	AsyncOverflowDropOldest AsyncOverflowPolicy = "drop_oldest"
)

var ErrAsyncSinkClosed = errors.New("cloudmanaged async audit sink is closed")

// AsyncSinkOverflowError reports that AsyncSink dropped at least one queued
// record to satisfy the configured overflow policy.
type AsyncSinkOverflowError struct {
	Policy AsyncOverflowPolicy
}

// Error implements error.
func (e *AsyncSinkOverflowError) Error() string {
	if e == nil || e.Policy == "" {
		return "cloudmanaged async audit sink overflow"
	}
	return fmt.Sprintf("cloudmanaged async audit sink overflow (%s)", e.Policy)
}

// AsyncSinkStats reports current queue pressure and cumulative queue activity.
//
// WrittenCount counts records accepted into the async queue. It does not mean
// the wrapped sink has already persisted all accepted records.
type AsyncSinkStats struct {
	WrittenCount int64
	DroppedCount int64
	QueueDepth   int
}

// AsyncSink asynchronously forwards audit records to an inner sink while
// preserving record order.
type AsyncSink struct {
	inner        AuditSink
	overflow     AsyncOverflowPolicy
	errorHandler AuditErrorHandler

	queue        chan asyncAuditItem
	closed       atomic.Bool
	writtenCount atomic.Int64
	droppedCount atomic.Int64
	closeOnce    sync.Once
	closedCh     chan struct{}
	done         chan struct{}
}

type asyncAuditItem struct {
	ctx    context.Context
	record AuditRecord
}

type asyncSinkConfig struct {
	bufferSize   int
	overflow     AsyncOverflowPolicy
	errorHandler AuditErrorHandler
}

// AsyncSinkOption configures AsyncSink.
type AsyncSinkOption func(*asyncSinkConfig) error

// WithAsyncSinkBufferSize overrides the async buffer size. Values <= 0 are
// rejected.
func WithAsyncSinkBufferSize(size int) AsyncSinkOption {
	return func(cfg *asyncSinkConfig) error {
		if size <= 0 {
			return fmt.Errorf("cloudmanaged async sink buffer size must be > 0")
		}
		cfg.bufferSize = size
		return nil
	}
}

// WithAsyncOverflowPolicy overrides the async overflow policy.
func WithAsyncOverflowPolicy(policy AsyncOverflowPolicy) AsyncSinkOption {
	return func(cfg *asyncSinkConfig) error {
		switch policy {
		case "", AsyncOverflowBlock:
			cfg.overflow = AsyncOverflowBlock
		case AsyncOverflowDropOldest:
			cfg.overflow = policy
		default:
			return fmt.Errorf("cloudmanaged async sink overflow policy %q is unsupported", policy)
		}
		return nil
	}
}

// WithAsyncSinkErrorHandler routes asynchronous sink failures and overflow
// notifications to handler. Handlers should stay fast and non-blocking: the
// drop-oldest overflow path invokes them inline with the caller's WriteAudit
// call, while sink-write failures are reported from the background worker.
func WithAsyncSinkErrorHandler(handler AuditErrorHandler) AsyncSinkOption {
	return func(cfg *asyncSinkConfig) error {
		cfg.errorHandler = handler
		return nil
	}
}

// NewAsyncSink wraps inner with asynchronous delivery and explicit drain
// semantics. The returned sink preserves record order, detaches queued records
// from caller cancellation while keeping context values, and requires Close to
// guarantee that buffered records are drained before process shutdown.
func NewAsyncSink(inner AuditSink, options ...AsyncSinkOption) (*AsyncSink, error) {
	if inner == nil {
		return nil, fmt.Errorf("cloudmanaged async sink inner sink is required")
	}
	cfg := asyncSinkConfig{
		bufferSize: defaultAsyncSinkBuffer,
		overflow:   AsyncOverflowBlock,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	sink := &AsyncSink{
		inner:        inner,
		overflow:     cfg.overflow,
		errorHandler: cfg.errorHandler,
		queue:        make(chan asyncAuditItem, cfg.bufferSize),
		closedCh:     make(chan struct{}),
		done:         make(chan struct{}),
	}
	go sink.run()
	return sink, nil
}

// Stats returns cheap, best-effort queue telemetry for the async sink.
func (s *AsyncSink) Stats() AsyncSinkStats {
	if s == nil {
		return AsyncSinkStats{}
	}
	return AsyncSinkStats{
		WrittenCount: s.writtenCount.Load(),
		DroppedCount: s.droppedCount.Load(),
		QueueDepth:   len(s.queue),
	}
}

// WriteAudit implements AuditSink.
func (s *AsyncSink) WriteAudit(ctx context.Context, record AuditRecord) error {
	if s == nil {
		return nil
	}
	if s.closed.Load() {
		return ErrAsyncSinkClosed
	}
	item := asyncAuditItem{
		ctx:    uncancelledContext(ctx),
		record: cloneAuditRecord(record),
	}
	switch s.overflow {
	case AsyncOverflowDropOldest:
		return s.writeDropOldest(ctx, item)
	default:
		return s.writeBlock(ctx, item)
	}
}

// Close stops accepting new writes, drains queued records in order, and waits
// for the background worker to exit.
func (s *AsyncSink) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.closedCh)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *AsyncSink) writeBlock(ctx context.Context, item asyncAuditItem) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.closedCh:
		return ErrAsyncSinkClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.queue <- item:
		s.writtenCount.Add(1)
		return nil
	}
}

func (s *AsyncSink) writeDropOldest(ctx context.Context, item asyncAuditItem) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		if s.closed.Load() {
			return ErrAsyncSinkClosed
		}
		select {
		case <-s.closedCh:
			return ErrAsyncSinkClosed
		case s.queue <- item:
			s.writtenCount.Add(1)
			return nil
		default:
		}
		select {
		case <-s.closedCh:
			return ErrAsyncSinkClosed
		case <-ctx.Done():
			return ctx.Err()
		case <-s.queue:
			s.droppedCount.Add(1)
			// Overflow notifications describe queue pressure rather than the
			// dropped record itself, so they use a detached context instead of the
			// caller's tracing scope.
			s.handleError(context.Background(), &AsyncSinkOverflowError{Policy: AsyncOverflowDropOldest})
		default:
		}
	}
}

func (s *AsyncSink) run() {
	defer close(s.done)
	for {
		select {
		case item := <-s.queue:
			if err := s.inner.WriteAudit(item.ctx, item.record); err != nil {
				s.handleError(item.ctx, err)
			}
		case <-s.closedCh:
			for {
				select {
				case item := <-s.queue:
					if err := s.inner.WriteAudit(item.ctx, item.record); err != nil {
						s.handleError(item.ctx, err)
					}
				default:
					return
				}
			}
		}
	}
}

func (s *AsyncSink) handleError(ctx context.Context, err error) {
	if err == nil || s.errorHandler == nil {
		return
	}
	s.errorHandler.HandleAuditError(ctx, err)
}

func uncancelledContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
