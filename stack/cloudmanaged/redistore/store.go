// Package redistore provides a Redis-backed cloudmanaged.QuotaStore.
//
// The store preserves the QuotaStore atomicity contract with a server-side Lua
// reservation script, so concurrent callers do not race through a client-side
// read/check/write sequence. Keys carry a TTL as a safety net for crashed runs:
// ResetSession still handles the normal cleanup path, but expired sessions do
// not accumulate forever when a process dies before it can run session-end
// hooks. Session and counter keys have independent TTLs, so sessions that stay
// idle longer than the TTL effectively reset their quota counters. Store
// instrumentation remains a host concern: wrap the Redis client if you need
// latency, error-rate, or replica-health telemetry for managed-service SLOs.
package redistore

import (
	"context"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/internal/scopekey"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/redis/go-redis/v9"
)

const (
	defaultKeyPrefix = "memax:cloudmanaged:quota"
	defaultTTL       = 24 * time.Hour
)

var reserveScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
local ttl = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
if ttl > 0 then
  redis.call("EXPIRE", KEYS[1], ttl)
end
if current > limit then
  redis.call("DECR", KEYS[1])
  return {current - 1, 0}
end
return {current, 1}
`)

// Store is a Redis-backed cloudmanaged.QuotaStore.
type Store struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// Option configures a Redis-backed quota store.
type Option func(*Store)

// WithKeyPrefix overrides the Redis key prefix. Empty keeps the default.
func WithKeyPrefix(prefix string) Option {
	return func(s *Store) {
		if s != nil && prefix != "" {
			s.keyPrefix = prefix
		}
	}
}

// WithTTL overrides the Redis TTL applied to session marker and quota keys.
// Zero or negative durations disable the expiry safety net.
func WithTTL(ttl time.Duration) Option {
	return func(s *Store) {
		if s != nil {
			s.ttl = ttl
		}
	}
}

// New constructs a Redis-backed quota store.
func New(client redis.UniversalClient, options ...Option) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("redis quota store client is required")
	}
	store := &Store{
		client:    client,
		keyPrefix: defaultKeyPrefix,
		ttl:       defaultTTL,
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store, nil
}

// EnsureSession implements cloudmanaged.QuotaStore.
func (s *Store) EnsureSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("redis quota store is nil")
	}
	if sessionID == "" {
		return nil
	}
	key := s.sessionKey(scope, sessionID)
	if s.ttl > 0 {
		return s.client.Set(ctx, key, "1", s.ttl).Err()
	}
	return s.client.Set(ctx, key, "1", 0).Err()
}

// Reserve implements cloudmanaged.QuotaStore.
func (s *Store) Reserve(ctx context.Context, scope tenant.Scope, sessionID string, counter cloudmanaged.QuotaCounter, limit int) (int, bool, error) {
	if err := contextError(ctx); err != nil {
		return 0, false, err
	}
	if s == nil {
		return 0, false, fmt.Errorf("redis quota store is nil")
	}
	if sessionID == "" || limit <= 0 {
		return 0, true, nil
	}
	if !knownCounter(counter) {
		return 0, false, fmt.Errorf("unknown quota counter %q", counter)
	}
	values, err := reserveScript.Run(ctx, s.client, []string{s.counterKey(scope, sessionID, counter)}, ttlSeconds(s.ttl), limit).Result()
	if err != nil {
		return 0, false, fmt.Errorf("reserve redis quota: %w", err)
	}
	parts, ok := values.([]any)
	if !ok || len(parts) != 2 {
		return 0, false, fmt.Errorf("reserve redis quota: unexpected script response %T", values)
	}
	used, ok := parts[0].(int64)
	if !ok {
		return 0, false, fmt.Errorf("reserve redis quota: unexpected usage value %T", parts[0])
	}
	granted, ok := parts[1].(int64)
	if !ok {
		return 0, false, fmt.Errorf("reserve redis quota: unexpected grant value %T", parts[1])
	}
	return int(used), granted == 1, nil
}

// ResetSession implements cloudmanaged.QuotaStore.
func (s *Store) ResetSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("redis quota store is nil")
	}
	if sessionID == "" {
		return nil
	}
	keys := []string{
		s.sessionKey(scope, sessionID),
		s.counterKey(scope, sessionID, cloudmanaged.QuotaCounterModelRequests),
		s.counterKey(scope, sessionID, cloudmanaged.QuotaCounterToolUses),
	}
	return s.client.Del(ctx, keys...).Err()
}

func (s *Store) sessionKey(scope tenant.Scope, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s:session", s.keyPrefix, scopekey.Digest(scope), sessionID)
}

func (s *Store) counterKey(scope tenant.Scope, sessionID string, counter cloudmanaged.QuotaCounter) string {
	return fmt.Sprintf("%s:%s:%s:%s", s.keyPrefix, scopekey.Digest(scope), sessionID, counter)
}

func ttlSeconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	return max(int64(ttl/time.Second), 1)
}

func knownCounter(counter cloudmanaged.QuotaCounter) bool {
	switch counter {
	case cloudmanaged.QuotaCounterModelRequests, cloudmanaged.QuotaCounterToolUses:
		return true
	default:
		return false
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
