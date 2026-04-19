package memaxagent

import (
	"context"
	"time"
)

// EventObserver observes emitted agent events as they happen. Observers are
// host-owned and non-authoritative: they can mirror events to logs, metrics,
// audit stores, or tracing systems without changing the event stream seen by
// callers. Observation runs synchronously on the event-emission path, so hosts
// with slow sinks should wrap them behind an async or buffered adapter to avoid
// backpressuring the agent loop.
type EventObserver interface {
	ObserveEvent(context.Context, Event)
}

// EventObserverFunc adapts a function to EventObserver.
type EventObserverFunc func(context.Context, Event)

// ObserveEvent calls f(ctx, event).
func (f EventObserverFunc) ObserveEvent(ctx context.Context, event Event) {
	if f != nil {
		f(ctx, event)
	}
}

type eventObserverContextKey struct{}

// WithEventObserver returns a child context that observes emitted agent events
// through observer. Nil observers leave ctx unchanged. Observation is scoped to
// the context value: code that creates a fresh context must reattach the
// observer if it wants events under that new context to remain observable.
func WithEventObserver(ctx context.Context, observer EventObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, eventObserverContextKey{}, observer)
}

func eventObserverFromContext(ctx context.Context) EventObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(eventObserverContextKey{}).(EventObserver)
	return observer
}

func observeEvent(ctx context.Context, event Event) {
	if observer := eventObserverFromContext(ctx); observer != nil {
		observer.ObserveEvent(ctx, event)
	}
}

// ObserveEvent sends one host-owned event through the observer attached to ctx.
// This is useful for optional stack-level lifecycle signals that are not
// emitted directly by Query/QueryAsync but should still flow through the same
// audit and observability seam.
func ObserveEvent(ctx context.Context, event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	observeEvent(ctx, event)
}

func observeStartupError(ctx context.Context, sessionID, parentSessionID string, err error) {
	if err == nil || eventObserverFromContext(ctx) == nil {
		return
	}
	if denied, ok := tenantDenied(err); ok {
		event := newEvent(EventTenantDenied, denied.Request.SessionID, 0)
		event.ParentSessionID = denied.Request.ParentSessionID
		event.Tenant = tenantEventFromRequest(denied.Request, denied.Error())
		observeEvent(ctx, event)
	}
	event := newEvent(EventError, sessionID, 0)
	event.ParentSessionID = parentSessionID
	event.Err = err
	observeEvent(ctx, event)
}
