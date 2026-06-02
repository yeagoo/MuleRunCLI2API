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
// Logging: every successful sweep that removes ≥1 row is logged at info;
// errors are logged at warn and do NOT terminate the loop.
func StartReaper(ctx context.Context, store Store, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		log.Info("reaper disabled", "interval", interval.String())
		return
	}
	go func() {
		log.Info("reaper started", "interval", interval.String())
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Info("reaper stopped")
				return
			case now := <-t.C:
				sweep(ctx, store, now.Unix(), log)
			}
		}
	}()
}

func sweep(ctx context.Context, store Store, now int64, log *slog.Logger) {
	n, err := store.DeleteExpired(ctx, now)
	if err != nil {
		log.Warn("reaper sweep failed", "err", err)
		return
	}
	if n > 0 {
		log.Info("reaper sweep", "deleted", n, "now", now)
	}
}
