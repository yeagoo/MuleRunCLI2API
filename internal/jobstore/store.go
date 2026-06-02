// Package jobstore persists async generation jobs (video, music) so that the
// IDs the server hands out survive a restart.
package jobstore

import (
	"context"
	"sync"
)

// Kind discriminates the surface a job belongs to. Used by handlers to
// reject lookups across surfaces (a /v1/videos/{id} call must not return a
// music job and vice versa).
type Kind string

const (
	KindVideo Kind = "video"
	KindMusic Kind = "music"
)

// Job is the canonical row.
type Job struct {
	LocalID      string
	Kind         Kind
	Model        string
	VendorPath   string
	VendorTaskID string
	CreatedAt    int64
	CompletedAt  int64
	ExpiresAt    int64 // unix seconds; 0 means never expires
	Status       string // queued | in_progress | completed | failed
	ResultURLs   []string
	ErrCode      int
	ErrMessage   string
}

// Store is the persistence interface.
type Store interface {
	Put(ctx context.Context, j *Job) error
	Get(ctx context.Context, id string) (*Job, error)
	DeleteExpired(ctx context.Context, now int64) (int64, error)
	Close() error
}

// Memory is a non-persistent map-backed implementation, suitable for local
// development and single-process testing. Restart loses state.
type Memory struct {
	mu   sync.Mutex
	jobs map[string]Job
}

func NewMemory() *Memory { return &Memory{jobs: map[string]Job{}} }

func (m *Memory) Put(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *j
	if j.ResultURLs != nil {
		clone.ResultURLs = append([]string(nil), j.ResultURLs...)
	}
	m.jobs[j.LocalID] = clone
	return nil
}

func (m *Memory) Get(_ context.Context, id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, nil
	}
	if j.ResultURLs != nil {
		j.ResultURLs = append([]string(nil), j.ResultURLs...)
	}
	return &j, nil
}

// DeleteExpired removes terminal jobs (completed / failed) whose ExpiresAt
// is non-zero and <= now. In-flight jobs are preserved even if expired so
// callers can still poll a long-running task.
func (m *Memory) DeleteExpired(_ context.Context, now int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, j := range m.jobs {
		if j.ExpiresAt > 0 && j.ExpiresAt <= now &&
			(j.Status == "completed" || j.Status == "failed") {
			delete(m.jobs, id)
			n++
		}
	}
	return n, nil
}

func (m *Memory) Close() error { return nil }
