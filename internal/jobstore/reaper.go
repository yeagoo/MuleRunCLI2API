package jobstore

import (
	"context"
	"log/slog"
	"time"
)

// StartReaper launches a background goroutine that periodically calls
// store.DeleteExpired. It returns when ctx is cancelled. interval <= 0
// disables the reaper (the goroutine simply returns immediately).
//
// hardCapMultiplier scales `now - expires_at` to compute the hard cutoff:
// a job is force-deleted regardless of status if expires_at < (now -
// retention*hardCapMultiplier). Default 3.0 — a job stuck in queued /
// in_progress past 4× its nominal retention is almost certainly a client
// that abandoned the polling loop, and we'd rather GC it than leak forever.
//
// Logging: every successful sweep that removes ≥1 row is logged at info;
// errors are logged at warn and do NOT terminate the loop.
func StartReaper(ctx context.Context, store Store, interval, retention time.Duration, hardCapMultiplier float64, log *slog.Logger) {
	if interval <= 0 {
		log.Info("reaper disabled", "interval", interval.String())
		return
	}
	if hardCapMultiplier < 1 {
		hardCapMultiplier = 1
	}
	hardLag := time.Duration(float64(retention) * hardCapMultiplier)
	go func() {
		log.Info("reaper started",
			"interval", interval.String(),
			"retention", retention.String(),
			"hard_cap_extra", hardLag.String(),
		)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Info("reaper stopped")
				return
			case now := <-t.C:
				hardCutoff := now.Add(-hardLag).Unix()
				sweep(ctx, store, now.Unix(), hardCutoff, log)
			}
		}
	}()
}

func sweep(ctx context.Context, store Store, now, hardCutoff int64, log *slog.Logger) {
	n, err := store.DeleteExpired(ctx, now, hardCutoff)
	if err != nil {
		log.Warn("reaper sweep failed", "err", err)
		return
	}
	if n > 0 {
		log.Info("reaper sweep", "deleted", n, "now", now, "hard_cutoff", hardCutoff)
	}
}
