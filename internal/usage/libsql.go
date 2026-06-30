package usage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// LibSQL is a libsql-backed Store. DSN conventions match jobstore.LibSQL:
//
//	file:/abs/path/usage.db       — local libsql/SQLite (pure-Go)
//	libsql://host?authToken=...   — Turso / sqld remote
//	http(s)://host?authToken=...  — sqld HTTP
type LibSQL struct {
	db *sql.DB
}

func OpenLibSQL(dsn string) (*LibSQL, error) {
	driver, dataSource := resolveDriver(dsn)
	db, err := sql.Open(driver, dataSource)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	s := &LibSQL{db: db}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func resolveDriver(dsn string) (driver, dataSource string) {
	switch {
	case strings.HasPrefix(dsn, "libsql://"),
		strings.HasPrefix(dsn, "http://"),
		strings.HasPrefix(dsn, "https://"),
		strings.HasPrefix(dsn, "wss://"),
		strings.HasPrefix(dsn, "ws://"):
		return "libsql", dsn
	case strings.HasPrefix(dsn, "file:"):
		// Force WAL + busy_timeout for concurrent reader/writer safety.
		// The async recorder inserts records while /v1/usage queries
		// scan the same table; without WAL mode SQLite serializes
		// readers behind writers and a single PRAGMA busy_timeout on
		// one connection doesn't propagate to other pool connections.
		// Pragmas must travel in the DSN to apply per-connection.
		return "sqlite", withConcurrencyPragmas(dsn)
	default:
		return "sqlite", withConcurrencyPragmas("file:"+url.PathEscape(dsn))
	}
}

// withConcurrencyPragmas appends WAL + busy_timeout pragmas to a sqlite
// file: DSN (preserving any existing ?query). modernc.org/sqlite reads
// `_pragma=` query keys on each new connection, so this guarantees every
// pooled connection inherits the same settings.
func withConcurrencyPragmas(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pragmas := "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if strings.Contains(dsn, "_pragma=") {
		// Caller already specified pragmas; respect them and only add
		// what's missing.
		out := dsn
		if !strings.Contains(dsn, "journal_mode") {
			out += sep + "_pragma=journal_mode(WAL)"
			sep = "&"
		}
		if !strings.Contains(dsn, "busy_timeout") {
			out += sep + "_pragma=busy_timeout(5000)"
		}
		return out
	}
	return dsn + sep + pragmas
}

// migrate uses a separate user_version table because PRAGMA user_version
// would clash if the usage store shares a DB file with jobstore. Schema
// per-table avoids both packages stepping on each other.
func (s *LibSQL) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS usage_records (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			ts                 INTEGER NOT NULL,
			endpoint           TEXT NOT NULL,
			model              TEXT NOT NULL DEFAULT '',
			status             INTEGER NOT NULL DEFAULT 0,
			duration_ms        INTEGER NOT NULL DEFAULT 0,
			bytes_out          INTEGER NOT NULL DEFAULT 0,
			prompt_tokens      INTEGER NOT NULL DEFAULT 0,
			completion_tokens  INTEGER NOT NULL DEFAULT 0,
			request_id         TEXT NOT NULL DEFAULT '',
			api_key_hash       TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_records(ts);
		CREATE INDEX IF NOT EXISTS idx_usage_ts_model ON usage_records(ts, model);
		CREATE TABLE IF NOT EXISTS usage_schema_version (
			version INTEGER NOT NULL
		);`,
	}

	var current int
	row := s.db.QueryRow(`SELECT version FROM usage_schema_version LIMIT 1`)
	if err := row.Scan(&current); err != nil && err != sql.ErrNoRows {
		// Table doesn't exist yet — run all migrations from scratch.
		current = 0
	}
	if current > len(migrations) {
		return fmt.Errorf("usage: db version=%d is newer than this binary supports (max=%d)", current, len(migrations))
	}
	for i := current; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("usage migration v%d: %w", i+1, err)
		}
	}
	// Upsert the recorded version.
	if _, err := s.db.Exec(`DELETE FROM usage_schema_version`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO usage_schema_version(version) VALUES (?)`, len(migrations)); err != nil {
		return err
	}
	return nil
}

func (s *LibSQL) Insert(ctx context.Context, r *Record) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO usage_records (ts, endpoint, model, status, duration_ms, bytes_out, prompt_tokens, completion_tokens, request_id, api_key_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		r.Timestamp.Unix(), r.Endpoint, r.Model, r.Status,
		r.DurationMs, r.BytesOut,
		r.PromptTokens, r.CompletionTokens,
		r.RequestID, r.APIKeyHash,
	)
	return err
}

// Aggregate runs the SQL aggregation. The group_by key is whitelisted
// against ValidGroupBy before composing the SQL — we never interpolate
// raw user input.
func (s *LibSQL) Aggregate(ctx context.Context, req AggregateRequest) ([]AggregateRow, Totals, error) {
	if _, ok := ValidGroupBy[req.GroupBy]; !ok {
		req.GroupBy = "model"
	}

	bucketExpr := bucketSQL(req.GroupBy)

	q := `
SELECT ` + bucketExpr + ` AS bucket,
       COUNT(*)                                              AS calls,
       SUM(CASE WHEN status/100 != 2 THEN 1 ELSE 0 END)       AS errors,
       COALESCE(SUM(prompt_tokens), 0)                        AS pt,
       COALESCE(SUM(completion_tokens), 0)                    AS ct,
       COALESCE(SUM(bytes_out), 0)                            AS bo
FROM usage_records
WHERE ts >= ? AND ts < ?`
	args := []any{req.From.Unix(), req.To.Unix()}
	if req.Model != "" {
		q += ` AND model = ?`
		args = append(args, req.Model)
	}
	if req.Endpoint != "" {
		q += ` AND endpoint = ?`
		args = append(args, req.Endpoint)
	}
	q += `
GROUP BY bucket
ORDER BY calls DESC, bucket ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, Totals{}, fmt.Errorf("usage aggregate: %w", err)
	}
	defer rows.Close()

	var out []AggregateRow
	var totals Totals
	for rows.Next() {
		var r AggregateRow
		if err := rows.Scan(&r.Bucket, &r.Calls, &r.Errors, &r.PromptTokens, &r.CompletionTokens, &r.BytesOut); err != nil {
			return nil, Totals{}, err
		}
		// Default-bucket for missing model in this group.
		if req.GroupBy == "model" && r.Bucket == "" {
			r.Bucket = "(unknown)"
		}
		out = append(out, r)
		totals.Calls += r.Calls
		totals.Errors += r.Errors
		totals.PromptTokens += r.PromptTokens
		totals.CompletionTokens += r.CompletionTokens
		totals.BytesOut += r.BytesOut
	}
	return out, totals, rows.Err()
}

// bucketSQL returns the SQL expression for the chosen group_by. Whitelist
// already enforced by Aggregate; this is purely a static dispatch.
func bucketSQL(groupBy string) string {
	switch groupBy {
	case "endpoint":
		return "endpoint"
	case "status":
		return "CAST(status AS TEXT)"
	case "day":
		return "strftime('%Y-%m-%d', ts, 'unixepoch')"
	case "hour":
		return "strftime('%Y-%m-%dT%H:00', ts, 'unixepoch')"
	case "model":
		fallthrough
	default:
		return "model"
	}
}

func (s *LibSQL) DeleteOlder(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM usage_records WHERE ts < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *LibSQL) Close() error { return s.db.Close() }
