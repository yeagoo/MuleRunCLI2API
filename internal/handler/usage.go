package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/openmule/cli2api/internal/usage"
	"github.com/openmule/cli2api/pkg/apierr"
)

// Usage returns GET /v1/usage. cli2api-specific endpoint that exposes
// the per-call records the recorder middleware writes — answering
// questions MuleRun's own dashboard doesn't (which model burned my
// credits last week, what's the call mix, etc).
//
// Query params (all optional):
//
//	from       — unix seconds OR RFC3339; default: 24h ago
//	to         — unix seconds OR RFC3339; default: now
//	group_by   — model | endpoint | status | day | hour (default: model)
//	model      — filter to one model
//	endpoint   — filter to one endpoint path
//
// Response:
//
//	{
//	  "from": "2026-06-28T08:00:00Z",
//	  "to":   "2026-06-29T08:00:00Z",
//	  "group_by": "model",
//	  "rows": [ { "bucket": "openai/gpt-5.5", "calls": 142, … } ],
//	  "totals": { "calls": 231, "errors": 3, … }
//	}
func Usage(store usage.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		now := time.Now()

		from, ok := parseTimeQuery(q.Get("from"), now.Add(-24*time.Hour))
		if !ok {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest,
				"from must be unix seconds or RFC3339", "invalid_request")
			return
		}
		to, ok := parseTimeQuery(q.Get("to"), now)
		if !ok {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest,
				"to must be unix seconds or RFC3339", "invalid_request")
			return
		}
		if !from.Before(to) {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest,
				"from must be strictly before to", "invalid_request")
			return
		}

		groupBy := q.Get("group_by")
		if groupBy == "" {
			groupBy = "model"
		}
		if _, ok := usage.ValidGroupBy[groupBy]; !ok {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest,
				"group_by must be one of model|endpoint|status|day|hour", "invalid_request")
			return
		}

		req := usage.AggregateRequest{
			From:     from,
			To:       to,
			GroupBy:  groupBy,
			Model:    q.Get("model"),
			Endpoint: q.Get("endpoint"),
		}
		rows, totals, err := store.Aggregate(r.Context(), req)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusInternalServerError,
				"usage query failed: "+err.Error(), "internal_error")
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"from":     from.UTC().Format(time.RFC3339),
			"to":       to.UTC().Format(time.RFC3339),
			"group_by": groupBy,
			"filter": map[string]string{
				"model":    req.Model,
				"endpoint": req.Endpoint,
			},
			"rows":   rows,
			"totals": totals,
		})
	})
}

// parseTimeQuery accepts either a unix-seconds integer or an RFC3339
// string. Empty string returns the supplied default.
func parseTimeQuery(s string, def time.Time) (time.Time, bool) {
	if s == "" {
		return def, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
