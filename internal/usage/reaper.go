package usage

import (
	"context"
	"log/slog"
	"time"
)

// StartReaper runs DeleteOlder on a ticker. retention<=0 disables the
// sweep (records are kept indefinitely — useful for short-lived
// processes or when an external system manages retention).
func StartReaper(ctx context.Context, store Store, interval, retention time.Duration, log *slog.Logger) {
	if interval <= 0 || retention <= 0 {
		log.Info("usage reaper disabled", "interval", interval, "retention", retention)
		return
	}
	log.Info("usage reaper started", "interval", interval, "retention", retention)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-retention)
				n, err := store.DeleteOlder(ctx, cutoff)
				if err != nil {
					log.Warn("usage reaper sweep failed", "err", err)
					continue
				}
				if n > 0 {
					log.Info("usage reaper swept", "removed", n, "cutoff", cutoff.Format(time.RFC3339))
				}
			}
		}
	}()
}
