// Package store persists indexed memory records into a SQLite database
// (WAL mode) with an FTS5 full-text index for search.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/josuebrunel/klodmem/internal/memoryfile"
	"github.com/josuebrunel/klodmem/internal/transcript"
)

const schema = `
CREATE TABLE IF NOT EXISTS files (
	path    TEXT PRIMARY KEY,
	project TEXT NOT NULL,
	name    TEXT NOT NULL,
	type    TEXT NOT NULL,
	mtime   INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
	path UNINDEXED,
	project,
	type,
	name,
	description,
	content,
	tokenize = 'porter'
);

CREATE TABLE IF NOT EXISTS transcript_files (
	path        TEXT PRIMARY KEY,
	project     TEXT NOT NULL,
	session_id  TEXT NOT NULL,
	byte_offset INTEGER NOT NULL,
	mtime       INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
	path UNINDEXED,
	session_id UNINDEXED,
	timestamp UNINDEXED,
	project,
	role,
	text,
	tokenize = 'porter'
);
`

// Store wraps a SQLite connection holding the memory index.
type Store struct {
	db *sql.DB
}

// Open creates the database file (and parent directory) if needed, enables
// WAL mode for concurrent multi-process access, and ensures the schema exists.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("store: create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dbPath, err)
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: apply pragma %q: %w", pragma, err)
		}
	}

	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// AllPaths returns every indexed file path, for pruning against what's
// actually present on disk.
func (s *Store) AllPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files`)
	if err != nil {
		return nil, fmt.Errorf("store: list paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("store: scan path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate paths: %w", err)
	}
	return paths, nil
}

// FileMTime returns the indexed mtime for path, and whether a row exists.
func (s *Store) FileMTime(ctx context.Context, path string) (int64, bool, error) {
	var mtime int64
	err := s.db.QueryRowContext(ctx, `SELECT mtime FROM files WHERE path = ?`, path).Scan(&mtime)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: query mtime for %s: %w", path, err)
	}
	return mtime, true, nil
}

// UpsertFile replaces the indexed record and FTS row for m.Path.
func (s *Store) UpsertFile(ctx context.Context, m memoryfile.Memory) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteFileTx(ctx, tx, m.Path); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO files (path, project, name, type, mtime) VALUES (?, ?, ?, ?, ?)`,
		m.Path, m.Project, m.Name, m.Type, m.MTime,
	); err != nil {
		return fmt.Errorf("store: insert file %s: %w", m.Path, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_fts (path, project, type, name, description, content) VALUES (?, ?, ?, ?, ?, ?)`,
		m.Path, m.Project, m.Type, m.Name, m.Description, m.Content,
	); err != nil {
		return fmt.Errorf("store: insert fts row for %s: %w", m.Path, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit upsert for %s: %w", m.Path, err)
	}
	return nil
}

// DeleteFile removes any indexed record and FTS row for path.
func (s *Store) DeleteFile(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteFileTx(ctx, tx, path); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete for %s: %w", path, err)
	}
	return nil
}

func deleteFileTx(ctx context.Context, tx *sql.Tx, path string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete file %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete fts row %s: %w", path, err)
	}
	return nil
}

// SearchQuery holds the parameters for a memory search.
type SearchQuery struct {
	Query   string
	Project string
	Type    string
	Limit   int
}

// SearchResult is one match returned from Search.
type SearchResult struct {
	Path        string
	Project     string
	Type        string
	Name        string
	Description string
	Snippet     string
}

// Search runs an FTS5 match against the index, optionally filtered by
// project/type, ordered by relevance.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	matchExpr := sanitizeFTSQuery(q.Query)
	if q.Project != "" {
		matchExpr = fmt.Sprintf("project:%s AND (%s)", quoteFTSTerm(q.Project), matchExpr)
	}
	if q.Type != "" {
		matchExpr = fmt.Sprintf("type:%s AND (%s)", quoteFTSTerm(q.Type), matchExpr)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT path, project, type, name, description,
		       snippet(memory_fts, 5, '[', ']', '...', 8) AS snip
		FROM memory_fts
		WHERE memory_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search %q: %w", q.Query, err)
	}
	defer func() { _ = rows.Close() }()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Path, &r.Project, &r.Type, &r.Name, &r.Description, &r.Snippet); err != nil {
			return nil, fmt.Errorf("store: scan search result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate search results: %w", err)
	}
	return results, nil
}

// TranscriptOffset returns the byte offset up to which path has already been
// parsed, and whether a row exists (a new file starts at offset 0).
func (s *Store) TranscriptOffset(ctx context.Context, path string) (int64, bool, error) {
	var offset int64
	err := s.db.QueryRowContext(ctx, `SELECT byte_offset FROM transcript_files WHERE path = ?`, path).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: query transcript offset for %s: %w", path, err)
	}
	return offset, true, nil
}

// InsertHistoryMessages records that path has been parsed up to newOffset
// and inserts msgs (the messages found in the newly-parsed span), all in one
// transaction. msgs may be empty (e.g. a span with no authored text).
func (s *Store) InsertHistoryMessages(ctx context.Context, path, project, sessionID string, newOffset, mtime int64, msgs []transcript.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin history insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transcript_files (path, project, session_id, byte_offset, mtime)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET byte_offset = excluded.byte_offset, mtime = excluded.mtime
	`, path, project, sessionID, newOffset, mtime); err != nil {
		return fmt.Errorf("store: upsert transcript offset for %s: %w", path, err)
	}

	for _, m := range msgs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO history_fts (path, session_id, timestamp, project, role, text) VALUES (?, ?, ?, ?, ?, ?)`,
			path, m.SessionID, m.Timestamp, m.Project, m.Role, m.Text,
		); err != nil {
			return fmt.Errorf("store: insert history message for %s: %w", path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit history insert for %s: %w", path, err)
	}
	return nil
}

// DeleteTranscript removes all indexed history for a transcript file, e.g.
// after the session file itself was deleted.
func (s *Store) DeleteTranscript(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transcript delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete transcript file %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_fts WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete history rows for %s: %w", path, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transcript delete for %s: %w", path, err)
	}
	return nil
}

// AllTranscriptPaths returns every indexed transcript file path, for pruning
// against what's actually present on disk.
func (s *Store) AllTranscriptPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM transcript_files`)
	if err != nil {
		return nil, fmt.Errorf("store: list transcript paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("store: scan transcript path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate transcript paths: %w", err)
	}
	return paths, nil
}

// HistorySearchQuery holds the parameters for a conversation-history search.
type HistorySearchQuery struct {
	Query   string
	Project string
	Role    string
	Limit   int
}

// HistorySearchResult is one match returned from SearchHistory.
type HistorySearchResult struct {
	Path      string
	SessionID string
	Timestamp string
	Project   string
	Role      string
	Snippet   string
}

// SearchHistory runs an FTS5 match against indexed conversation history,
// optionally filtered by project/role, ordered by relevance.
func (s *Store) SearchHistory(ctx context.Context, q HistorySearchQuery) ([]HistorySearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	matchExpr := sanitizeFTSQuery(q.Query)
	if q.Project != "" {
		matchExpr = fmt.Sprintf("project:%s AND (%s)", quoteFTSTerm(q.Project), matchExpr)
	}
	if q.Role != "" {
		matchExpr = fmt.Sprintf("role:%s AND (%s)", quoteFTSTerm(q.Role), matchExpr)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT path, session_id, timestamp, project, role,
		       snippet(history_fts, 5, '[', ']', '...', 12) AS snip
		FROM history_fts
		WHERE history_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search history %q: %w", q.Query, err)
	}
	defer func() { _ = rows.Close() }()

	var results []HistorySearchResult
	for rows.Next() {
		var r HistorySearchResult
		if err := rows.Scan(&r.Path, &r.SessionID, &r.Timestamp, &r.Project, &r.Role, &r.Snippet); err != nil {
			return nil, fmt.Errorf("store: scan history search result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate history search results: %w", err)
	}
	return results, nil
}

// ProjectStat is one project's contribution to the history index.
type ProjectStat struct {
	Project  string
	Messages int
	Sessions int
}

// Stats summarizes what's currently indexed.
type Stats struct {
	MemoryFiles     int
	HistoryMessages int
	HistorySessions int
	HistoryProjects int
	RoleCounts      map[string]int
	ByProject       []ProjectStat
	EarliestTime    string
	LatestTime      string
}

// Stats runs aggregate queries over the index and returns a summary. It
// reflects whatever is currently indexed — it does not scan disk itself.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&stats.MemoryFiles); err != nil {
		return Stats{}, fmt.Errorf("store: count memory files: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT session_id), COUNT(DISTINCT project) FROM history_fts
	`).Scan(&stats.HistoryMessages, &stats.HistorySessions, &stats.HistoryProjects); err != nil {
		return Stats{}, fmt.Errorf("store: count history: %w", err)
	}

	roleRows, err := s.db.QueryContext(ctx, `SELECT role, COUNT(*) FROM history_fts GROUP BY role`)
	if err != nil {
		return Stats{}, fmt.Errorf("store: count history by role: %w", err)
	}
	defer func() { _ = roleRows.Close() }()
	stats.RoleCounts = make(map[string]int)
	for roleRows.Next() {
		var role string
		var n int
		if err := roleRows.Scan(&role, &n); err != nil {
			return Stats{}, fmt.Errorf("store: scan role count: %w", err)
		}
		stats.RoleCounts[role] = n
	}
	if err := roleRows.Err(); err != nil {
		return Stats{}, fmt.Errorf("store: iterate role counts: %w", err)
	}

	projectRows, err := s.db.QueryContext(ctx, `
		SELECT project, COUNT(*), COUNT(DISTINCT session_id)
		FROM history_fts
		GROUP BY project
		ORDER BY COUNT(*) DESC
	`)
	if err != nil {
		return Stats{}, fmt.Errorf("store: count history by project: %w", err)
	}
	defer func() { _ = projectRows.Close() }()
	for projectRows.Next() {
		var p ProjectStat
		if err := projectRows.Scan(&p.Project, &p.Messages, &p.Sessions); err != nil {
			return Stats{}, fmt.Errorf("store: scan project stat: %w", err)
		}
		stats.ByProject = append(stats.ByProject, p)
	}
	if err := projectRows.Err(); err != nil {
		return Stats{}, fmt.Errorf("store: iterate project stats: %w", err)
	}

	var earliest, latest sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(timestamp), MAX(timestamp) FROM history_fts`).Scan(&earliest, &latest); err != nil {
		return Stats{}, fmt.Errorf("store: query history time range: %w", err)
	}
	stats.EarliestTime = earliest.String
	stats.LatestTime = latest.String

	return stats, nil
}

func quoteFTSTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

// sanitizeFTSQuery quotes each whitespace-separated token of a free-text
// query so that FTS5's special characters (-, :, *, etc.) inside a token
// like "table-driven" are treated as literal phrase text instead of query
// syntax (a bare "-" prefix means NOT in FTS5's MATCH grammar).
func sanitizeFTSQuery(query string) string {
	fields := strings.Fields(query)
	for i, f := range fields {
		fields[i] = quoteFTSTerm(f)
	}
	return strings.Join(fields, " ")
}
