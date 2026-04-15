package skill

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TimeoutSource bounds another source with a timeout.
type TimeoutSource struct {
	Source  Source
	Timeout time.Duration
}

// Skills loads skills through Source with the configured timeout.
func (s TimeoutSource) Skills(ctx context.Context) ([]Skill, error) {
	if s.Source == nil {
		return nil, fmt.Errorf("skill: timeout source requires Source")
	}
	if s.Timeout <= 0 {
		return s.Source.Skills(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.Source.Skills(ctx)
}

// PrefetchSource serves the last successful skill snapshot and refreshes stale
// snapshots in the background.
//
// The first load is synchronous because there is no snapshot to serve yet.
// Subsequent expired reads return stale skills immediately and trigger a single
// background refresh shared by concurrent callers.
type PrefetchSource struct {
	Source         Source
	TTL            time.Duration
	RefreshTimeout time.Duration

	mu         sync.Mutex
	skills     []Skill
	expiresAt  time.Time
	refreshing bool
	ready      chan struct{}
	lastErr    error
}

// Skills returns the current skill snapshot. If the snapshot is stale, a
// background refresh is started and the stale snapshot is returned.
func (s *PrefetchSource) Skills(ctx context.Context) ([]Skill, error) {
	if s == nil || s.Source == nil {
		return nil, fmt.Errorf("skill: prefetch source requires Source")
	}

	for {
		s.mu.Lock()
		if s.skills != nil {
			if s.expiredLocked(time.Now()) && !s.refreshing {
				s.startBackgroundRefreshLocked()
			}
			out := cloneSkills(s.skills)
			s.mu.Unlock()
			return out, nil
		}
		if s.refreshing {
			ready := s.ready
			s.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.refreshing = true
		s.ready = make(chan struct{})
		s.mu.Unlock()
		return s.loadForeground(ctx)
	}
}

// Refresh forces a synchronous refresh. It is useful during server startup.
func (s *PrefetchSource) Refresh(ctx context.Context) error {
	if s == nil || s.Source == nil {
		return fmt.Errorf("skill: prefetch source requires Source")
	}
	for {
		s.mu.Lock()
		if s.refreshing {
			ready := s.ready
			s.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		s.refreshing = true
		s.ready = make(chan struct{})
		s.mu.Unlock()
		_, err := s.loadForeground(ctx)
		return err
	}
}

// LastError returns the most recent refresh error, if any.
func (s *PrefetchSource) LastError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *PrefetchSource) loadForeground(ctx context.Context) ([]Skill, error) {
	loaded, err := s.Source.Skills(ctx)
	s.finishRefresh(loaded, err)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	out := cloneSkills(s.skills)
	s.mu.Unlock()
	return out, nil
}

func (s *PrefetchSource) startBackgroundRefreshLocked() {
	s.refreshing = true
	s.ready = make(chan struct{})
	go func() {
		ctx := context.Background()
		var cancel context.CancelFunc
		if s.RefreshTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, s.RefreshTimeout)
		}
		if cancel != nil {
			defer cancel()
		}
		loaded, err := s.Source.Skills(ctx)
		s.finishRefresh(loaded, err)
	}()
}

func (s *PrefetchSource) finishRefresh(loaded []Skill, err error) {
	loaded = cloneSkills(loaded)
	s.mu.Lock()
	ready := s.ready
	if err == nil {
		s.skills = loaded
		if s.TTL > 0 {
			s.expiresAt = time.Now().Add(s.TTL)
		} else {
			s.expiresAt = time.Time{}
		}
	}
	s.lastErr = err
	s.refreshing = false
	s.ready = nil
	if ready != nil {
		close(ready)
	}
	s.mu.Unlock()
}

func (s *PrefetchSource) expiredLocked(now time.Time) bool {
	return s.TTL > 0 && !now.Before(s.expiresAt)
}
