package jobstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// LibSQL is a libsql-backed implementation. The DSN follows the libsql
// driver's convention:
//
//	file:/abs/path/jobs.db       — local libsql/SQLite file (pure-Go, no CGO)
//	libsql://host?authToken=...  — Turso / sqld remote
//	http(s)://host?authToken=... — sqld HTTP
//
// Local-file DSNs are auto-rewritten to use the modernc.org/sqlite driver so
// that no extra dependency is required for the common case.
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
		// modernc.org/sqlite parses file: URIs directly.
		return "sqlite", dsn
	default:
		// Assume a plain filesystem path.
		return "sqlite", "file:" + url.PathEscape(dsn) + "?_pragma=busy_timeout(5000)"
	}
}

func (s *LibSQL) migrate() error {
	// Versioned migrations. PRAGMA user_version is per-database persistent
	// state, so once a step runs it's never re-run on the same file.
	migrations := []string{
		// v1: original schema
		`CREATE TABLE IF NOT EXISTS jobs (
			local_id       TEXT PRIMARY KEY,
			kind           TEXT NOT NULL,
			model          TEXT NOT NULL,
			vendor_path    TEXT NOT NULL,
			vendor_task_id TEXT NOT NULL,
			created_at     INTEGER NOT NULL,
			completed_at   INTEGER NOT NULL DEFAULT 0,
			status         TEXT NOT NULL,
			result_urls    TEXT,
			err_code       INTEGER NOT NULL DEFAULT 0,
			err_message    TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);`,

		// v2: add expires_at + index for reaper
		`ALTER TABLE jobs ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
		CREATE INDEX IF NOT EXISTS idx_jobs_expires ON jobs(expires_at);`,
	}

	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	// If the DB was written by a newer cli2api (user_version > what we know),
	// refuse to start rather than reading/writing it with stale schema
	// assumptions. The operator should either upgrade or roll back the DB.
	if current > len(migrations) {
		return fmt.Errorf("jobstore: db user_version=%d is newer than this binary supports (max=%d); upgrade cli2api or remove the db file", current, len(migrations))
	}

	for i := current; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return fmt.Errorf("bump user_version to %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *LibSQL) Put(ctx context.Context, j *Job) error {
	urlsJSON := "[]"
	if len(j.ResultURLs) > 0 {
		b, err := json.Marshal(j.ResultURLs)
		if err != nil {
			return err
		}
		urlsJSON = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO jobs (local_id, kind, model, vendor_path, vendor_task_id, created_at, completed_at, expires_at, status, result_urls, err_code, err_message)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(local_id) DO UPDATE SET
  status = excluded.status,
  completed_at = excluded.completed_at,
  expires_at = excluded.expires_at,
  result_urls = excluded.result_urls,
  err_code = excluded.err_code,
  err_message = excluded.err_message
`, j.LocalID, string(j.Kind), j.Model, j.VendorPath, j.VendorTaskID, j.CreatedAt, j.CompletedAt, j.ExpiresAt, j.Status, urlsJSON, j.ErrCode, j.ErrMessage)
	return err
}

func (s *LibSQL) Get(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT local_id, kind, model, vendor_path, vendor_task_id, created_at, completed_at, expires_at, status, result_urls, err_code, err_message
FROM jobs WHERE local_id = ?`, id)
	var j Job
	var kind string
	var urls string
	if err := row.Scan(&j.LocalID, &kind, &j.Model, &j.VendorPath, &j.VendorTaskID,
		&j.CreatedAt, &j.CompletedAt, &j.ExpiresAt, &j.Status, &urls, &j.ErrCode, &j.ErrMessage); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	j.Kind = Kind(kind)
	if urls != "" && urls != "[]" {
		if err := json.Unmarshal([]byte(urls), &j.ResultURLs); err != nil {
			return nil, fmt.Errorf("decode result_urls: %w", err)
		}
	}
	return &j, nil
}

func (s *LibSQL) DeleteExpired(ctx context.Context, now, hardCutoff int64) (int64, error) {
	// hardCutoff <= 0 disables the hard-cap branch entirely so callers can
	// run a "soft" sweep without affecting in-flight jobs.
	res, err := s.db.ExecContext(ctx, `
DELETE FROM jobs WHERE expires_at > 0 AND (
  (expires_at <= ? AND status IN ('completed', 'failed'))
  OR (? > 0 AND expires_at <= ?)
)`, now, hardCutoff, hardCutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *LibSQL) Close() error { return s.db.Close() }
