package usage

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is the in-memory store used when CLI2API_USAGE_DSN is empty.
// Records are lost on restart; suitable for dev/short-lived deployments.
type Memory struct {
	mu      sync.RWMutex
	records []Record
}

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Insert(_ context.Context, r *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, *r)
	return nil
}

func (m *Memory) Aggregate(_ context.Context, req AggregateRequest) ([]AggregateRow, Totals, error) {
	if _, ok := ValidGroupBy[req.GroupBy]; !ok {
		req.GroupBy = "model"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	buckets := map[string]*AggregateRow{}
	var totals Totals

	for i := range m.records {
		r := &m.records[i]
		if !r.Timestamp.Before(req.To) {
			continue
		}
		if r.Timestamp.Before(req.From) {
			continue
		}
		if req.Model != "" && r.Model != req.Model {
			continue
		}
		if req.Endpoint != "" && r.Endpoint != req.Endpoint {
			continue
		}
		key := bucketKey(r, req.GroupBy)
		row, ok := buckets[key]
		if !ok {
			row = &AggregateRow{Bucket: key}
			buckets[key] = row
		}
		row.Calls++
		row.PromptTokens += int64(r.PromptTokens)
		row.CompletionTokens += int64(r.CompletionTokens)
		row.BytesOut += r.BytesOut
		if r.Status/100 != 2 {
			row.Errors++
		}

		totals.Calls++
		totals.PromptTokens += int64(r.PromptTokens)
		totals.CompletionTokens += int64(r.CompletionTokens)
		totals.BytesOut += r.BytesOut
		if r.Status/100 != 2 {
			totals.Errors++
		}
	}

	rows := make([]AggregateRow, 0, len(buckets))
	for _, row := range buckets {
		rows = append(rows, *row)
	}
	// Deterministic order: descending Calls, then alphabetical bucket.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Calls != rows[j].Calls {
			return rows[i].Calls > rows[j].Calls
		}
		return rows[i].Bucket < rows[j].Bucket
	})
	return rows, totals, nil
}

func (m *Memory) DeleteOlder(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.records[:0]
	var removed int64
	for _, r := range m.records {
		if r.Timestamp.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	m.records = kept
	return removed, nil
}

func (m *Memory) Close() error { return nil }

// bucketKey computes the group_by key for one record.
func bucketKey(r *Record, groupBy string) string {
	switch groupBy {
	case "endpoint":
		return r.Endpoint
	case "status":
		return statusBucket(r.Status)
	case "day":
		return r.Timestamp.UTC().Format("2006-01-02")
	case "hour":
		return r.Timestamp.UTC().Format("2006-01-02T15:00")
	case "model":
		fallthrough
	default:
		if r.Model == "" {
			return "(unknown)"
		}
		return r.Model
	}
}

func statusBucket(s int) string {
	if s == 0 {
		return "(no-status)"
	}
	// Use the raw status — callers grouping by status usually want exact
	// codes (200 vs 502 vs 413), not just classes.
	return itoa(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
