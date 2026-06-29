package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Store contract — both Memory and LibSQL must satisfy the same shape.

func newStores(t *testing.T) map[string]Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	lib, err := OpenLibSQL("file:" + dbPath)
	if err != nil {
		t.Fatalf("open libsql: %v", err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	return map[string]Store{
		"memory": NewMemory(),
		"libsql": lib,
	}
}

func TestStore_InsertAndAggregateByModel(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()

			records := []Record{
				{Timestamp: now.Add(-2 * time.Hour), Endpoint: "/v1/chat/completions", Model: "gpt-5.5", Status: 200, PromptTokens: 100, CompletionTokens: 50, BytesOut: 1000},
				{Timestamp: now.Add(-1 * time.Hour), Endpoint: "/v1/chat/completions", Model: "gpt-5.5", Status: 200, PromptTokens: 150, CompletionTokens: 75, BytesOut: 1500},
				{Timestamp: now.Add(-30 * time.Minute), Endpoint: "/v1/chat/completions", Model: "deepseek-v4-flash", Status: 200, PromptTokens: 80, CompletionTokens: 40, BytesOut: 800},
				{Timestamp: now.Add(-10 * time.Minute), Endpoint: "/v1/chat/completions", Model: "deepseek-v4-flash", Status: 502, PromptTokens: 0, CompletionTokens: 0, BytesOut: 100},
			}
			for i := range records {
				if err := s.Insert(ctx, &records[i]); err != nil {
					t.Fatalf("insert: %v", err)
				}
			}

			rows, totals, err := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-3 * time.Hour),
				To:      now.Add(time.Hour),
				GroupBy: "model",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
			}
			// Both models: 2 calls each. Ordering by calls DESC then bucket ASC
			// puts deepseek-v4-flash first alphabetically when ties.
			if rows[0].Bucket != "deepseek-v4-flash" {
				t.Fatalf("expected deepseek-v4-flash first (alpha tiebreak), got %s", rows[0].Bucket)
			}
			if rows[1].Bucket != "gpt-5.5" {
				t.Fatalf("expected gpt-5.5 second, got %s", rows[1].Bucket)
			}
			// gpt-5.5 totals
			gpt := findBucket(rows, "gpt-5.5")
			if gpt.Calls != 2 || gpt.PromptTokens != 250 || gpt.CompletionTokens != 125 || gpt.Errors != 0 {
				t.Fatalf("gpt-5.5 row wrong: %+v", gpt)
			}
			// deepseek totals — one 502
			ds := findBucket(rows, "deepseek-v4-flash")
			if ds.Calls != 2 || ds.Errors != 1 {
				t.Fatalf("deepseek row wrong: %+v", ds)
			}
			if totals.Calls != 4 || totals.Errors != 1 || totals.PromptTokens != 330 {
				t.Fatalf("totals wrong: %+v", totals)
			}
		})
	}
}

func TestStore_AggregateWithModelFilter(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			_ = s.Insert(ctx, &Record{Timestamp: now, Endpoint: "/v1/chat/completions", Model: "gpt-5.5", Status: 200, PromptTokens: 10})
			_ = s.Insert(ctx, &Record{Timestamp: now, Endpoint: "/v1/chat/completions", Model: "deepseek-v4-flash", Status: 200, PromptTokens: 20})

			rows, totals, err := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-1 * time.Hour),
				To:      now.Add(time.Hour),
				GroupBy: "model",
				Model:   "gpt-5.5",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Bucket != "gpt-5.5" {
				t.Fatalf("model filter not applied: %+v", rows)
			}
			if totals.PromptTokens != 10 {
				t.Fatalf("totals should reflect only filtered: %+v", totals)
			}
		})
	}
}

func TestStore_AggregateGroupByStatus(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			for _, st := range []int{200, 200, 502, 413} {
				_ = s.Insert(ctx, &Record{Timestamp: now, Endpoint: "/v1/chat/completions", Model: "x", Status: st})
			}
			rows, _, _ := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-time.Hour),
				To:      now.Add(time.Hour),
				GroupBy: "status",
			})
			if len(rows) != 3 {
				t.Fatalf("want 3 status buckets, got %d: %+v", len(rows), rows)
			}
		})
	}
}

func TestStore_TimeWindowExcludesOutOfRange(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			_ = s.Insert(ctx, &Record{Timestamp: now.Add(-2 * time.Hour), Endpoint: "/v1/x", Model: "m", Status: 200})
			_ = s.Insert(ctx, &Record{Timestamp: now.Add(-30 * time.Minute), Endpoint: "/v1/x", Model: "m", Status: 200})

			rows, totals, _ := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-1 * time.Hour),
				To:      now,
				GroupBy: "model",
			})
			if totals.Calls != 1 {
				t.Fatalf("want 1 call in window, got %d (rows=%+v)", totals.Calls, rows)
			}
		})
	}
}

func TestStore_DeleteOlder(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			_ = s.Insert(ctx, &Record{Timestamp: now.Add(-48 * time.Hour), Endpoint: "/v1/x", Model: "m", Status: 200})
			_ = s.Insert(ctx, &Record{Timestamp: now.Add(-1 * time.Hour), Endpoint: "/v1/x", Model: "m", Status: 200})

			removed, err := s.DeleteOlder(ctx, now.Add(-24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if removed != 1 {
				t.Fatalf("want 1 removed, got %d", removed)
			}
			_, totals, _ := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-100 * time.Hour),
				To:      now.Add(time.Hour),
				GroupBy: "model",
			})
			if totals.Calls != 1 {
				t.Fatalf("want 1 remaining, got %d", totals.Calls)
			}
		})
	}
}

// Whitelist guard — invalid group_by falls back rather than error.
func TestStore_AggregateUnknownGroupByFallsBack(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			_ = s.Insert(ctx, &Record{Timestamp: now, Endpoint: "/v1/x", Model: "m", Status: 200})
			rows, _, err := s.Aggregate(ctx, AggregateRequest{
				From:    now.Add(-time.Hour),
				To:      now.Add(time.Hour),
				GroupBy: "1=1; DROP TABLE usage_records;",
			})
			if err != nil {
				t.Fatalf("must not error on bad group_by (got %v) — must fall back", err)
			}
			if len(rows) != 1 || rows[0].Bucket != "m" {
				t.Fatalf("expected fallback to model, got %+v", rows)
			}
		})
	}
}

func findBucket(rows []AggregateRow, bucket string) AggregateRow {
	for _, r := range rows {
		if r.Bucket == bucket {
			return r
		}
	}
	return AggregateRow{}
}
