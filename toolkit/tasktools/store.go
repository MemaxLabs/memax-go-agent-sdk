package tasktools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusBlocked    Status = "blocked"
	StatusCanceled   Status = "canceled"
)

type Task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   Status `json:"status"`
	Notes    string `json:"notes,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

type Store interface {
	List(context.Context) ([]Task, error)
	Upsert(context.Context, Task) (Task, error)
	Delete(context.Context, string) error
}

type MemoryStore struct {
	mu    sync.RWMutex
	next  int
	order []string
	tasks map[string]Task
}

func NewMemoryStore(tasks []Task) *MemoryStore {
	store := &MemoryStore{tasks: make(map[string]Task), next: 1}
	for _, task := range tasks {
		_, _ = store.Upsert(context.Background(), task)
	}
	return store
}

func (s *MemoryStore) List(context.Context) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.order))
	for _, id := range s.order {
		task, ok := s.tasks[id]
		if ok {
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *MemoryStore) Upsert(_ context.Context, task Task) (Task, error) {
	task.ID = strings.TrimSpace(task.ID)
	task.Title = strings.TrimSpace(task.Title)
	task.Notes = strings.TrimSpace(task.Notes)
	if !isValidStatus(task.Status) {
		return Task{}, fmt.Errorf("invalid task status: %s", task.Status)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if task.ID == "" {
		if task.Title == "" {
			return Task{}, fmt.Errorf("task title is required")
		}
		task.ID = s.nextIDLocked()
		if task.Status == "" {
			task.Status = StatusPending
		}
	} else if existing, ok := s.tasks[task.ID]; ok {
		task = mergeTask(existing, task)
	} else if task.Title == "" {
		return Task{}, fmt.Errorf("task title is required")
	} else if task.Status == "" {
		task.Status = StatusPending
	}
	if !isValidStatus(task.Status) {
		return Task{}, fmt.Errorf("invalid task status: %s", task.Status)
	}
	if _, ok := s.tasks[task.ID]; !ok {
		s.order = append(s.order, task.ID)
	}
	s.tasks[task.ID] = task
	s.bumpNextLocked(task.ID)
	return task, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return fmt.Errorf("task not found: %s", id)
	}
	delete(s.tasks, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

func (s *MemoryStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("task-%d", s.next)
		s.next++
		if _, ok := s.tasks[id]; !ok {
			return id
		}
	}
}

func (s *MemoryStore) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "task-%d", &n); err == nil && n >= s.next {
		s.next = n + 1
	}
}

func mergeTask(existing Task, update Task) Task {
	if update.Title != "" {
		existing.Title = update.Title
	}
	if update.Status != "" {
		existing.Status = update.Status
	}
	if update.Notes != "" {
		existing.Notes = update.Notes
	}
	if update.Priority > 0 {
		existing.Priority = update.Priority
	}
	return existing
}

func isValidStatus(status Status) bool {
	switch status {
	case "", StatusPending, StatusInProgress, StatusCompleted, StatusBlocked, StatusCanceled:
		return true
	default:
		return false
	}
}

func sortTasks(tasks []Task) []Task {
	out := append([]Task(nil), tasks...)
	sort.SliceStable(out, func(i int, j int) bool {
		left := out[i]
		right := out[j]
		if left.Priority != right.Priority {
			if left.Priority == 0 {
				return false
			}
			if right.Priority == 0 {
				return true
			}
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
	return out
}
