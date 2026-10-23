// Package store persists indexed memory, conversation-history, prompt-history,
// and plan records into a SQLite database (WAL mode) with FTS5 full-text
// indexes for search.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/josuebrunel/klodmem/internal/memoryfile"
	"github.com/josuebrunel/klodmem/internal/planfile"
	"github.com/josuebrunel/klodmem/internal/promptfile"
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

-- history_messages holds one row per indexed message, keyed by the message's
-- identity within its transcript. It exists so that indexing the same message
-- twice is a no-op: history_fts is an FTS5 table and can't carry a unique
-- constraint of its own.
CREATE TABLE IF NOT EXISTS history_messages (
	path      TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	role      TEXT NOT NULL,
	text_hash TEXT NOT NULL,
	PRIMARY KEY (path, timestamp, role, text_hash)
);

-- plan_files and plan_fts hold Plan Mode documents (~/.claude/plans/*.md).
CREATE TABLE IF NOT EXISTS plan_files (
	path        TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL,
	mtime       INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS plan_fts USING fts5(
	path UNINDEXED,
	name,
	description,
	content,
	tokenize = 'porter'
);

-- prompt_files/prompt_fts/prompt_messages hold the global prompt history
-- (~/.claude/history.jsonl), indexed incrementally like transcripts.
CREATE TABLE IF NOT EXISTS prompt_files (
	path        TEXT PRIMARY KEY,
	byte_offset INTEGER NOT NULL,
	mtime       INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS prompt_fts USING fts5(
	path UNINDEXED,
	session_id UNINDEXED,
	timestamp UNINDEXED,
	project,
	text,
	tokenize = 'porter'
);

CREATE TABLE IF NOT EXISTS prompt_messages (
	path       TEXT NOT NULL,
	timestamp  TEXT NOT NULL,
	session_id TEXT NOT NULL,
	text_hash  TEXT NOT NULL,
	PRIMARY KEY (path, timestamp, session_id, text_hash)
);

-- tool_calls records one row per MCP tool invocation. Unlike the tables
-- above, it is usage data and is not rebuilt from files, so -reset leaves it
-- intact.
CREATE TABLE IF NOT EXISTS tool_calls (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ts           INTEGER NOT NULL,
	tool         TEXT NOT NULL,
	query        TEXT NOT NULL DEFAULT '',
	project      TEXT NOT NULL DEFAULT '',
	result_count INTEGER NOT NULL DEFAULT 0,
	is_error     INTEGER NOT NULL DEFAULT 0,
	duration_ms  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_ts ON tool_calls(ts);
CREATE INDEX IF NOT EXISTS idx_tool_calls_tool ON tool_calls(tool);
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
	if matchExpr == "" {
		return nil, fmt.Errorf("store: search: empty query")
	}
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
//
// Messages already indexed for path are skipped, so re-parsing a span that
// was indexed before (a re-scan of a rewritten transcript, or a second
// klodmem process indexing the same span at the same time) adds nothing.
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
		if err := insertHistoryMessage(ctx, tx, path, m); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit history insert for %s: %w", path, err)
	}
	return nil
}

// insertHistoryMessage indexes one message, skipping it if the message is
// already indexed for path. The history_messages row doubles as the claim:
// its primary key is the message's identity, so a duplicate insert affects
// no rows and the FTS row is never written.
func insertHistoryMessage(ctx context.Context, tx *sql.Tx, path string, m transcript.Message) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO history_messages (path, timestamp, role, text_hash)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, path, m.Timestamp, m.Role, textHash(m.Text))
	if err != nil {
		return fmt.Errorf("store: claim history message for %s: %w", path, err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: check history message claim for %s: %w", path, err)
	}
	if inserted == 0 {
		return nil // already indexed
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO history_fts (path, session_id, timestamp, project, role, text) VALUES (?, ?, ?, ?, ?, ?)`,
		path, m.SessionID, m.Timestamp, m.Project, m.Role, m.Text,
	); err != nil {
		return fmt.Errorf("store: insert history message for %s: %w", path, err)
	}
	return nil
}

// textHash identifies a message's text within history_messages. Truncated
// message text is hashed as stored, so it also matches on re-reads.
func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_messages WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete history message keys for %s: %w", path, err)
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
	if matchExpr == "" {
		return nil, fmt.Errorf("store: search history: empty query")
	}
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

// AllPlanPaths returns every indexed plan file path, for pruning against
// what's actually present on disk.
func (s *Store) AllPlanPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM plan_files`)
	if err != nil {
		return nil, fmt.Errorf("store: list plan paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("store: scan plan path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate plan paths: %w", err)
	}
	return paths, nil
}

// PlanMTime returns the indexed mtime for path, and whether a row exists.
func (s *Store) PlanMTime(ctx context.Context, path string) (int64, bool, error) {
	var mtime int64
	err := s.db.QueryRowContext(ctx, `SELECT mtime FROM plan_files WHERE path = ?`, path).Scan(&mtime)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: query plan mtime for %s: %w", path, err)
	}
	return mtime, true, nil
}

// UpsertPlan replaces the indexed record and FTS row for p.Path.
func (s *Store) UpsertPlan(ctx context.Context, p planfile.Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin plan upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deletePlanTx(ctx, tx, p.Path); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plan_files (path, name, description, mtime) VALUES (?, ?, ?, ?)`,
		p.Path, p.Name, p.Description, p.MTime,
	); err != nil {
		return fmt.Errorf("store: insert plan %s: %w", p.Path, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plan_fts (path, name, description, content) VALUES (?, ?, ?, ?)`,
		p.Path, p.Name, p.Description, p.Content,
	); err != nil {
		return fmt.Errorf("store: insert plan fts row for %s: %w", p.Path, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit plan upsert for %s: %w", p.Path, err)
	}
	return nil
}

// DeletePlan removes any indexed record and FTS row for path.
func (s *Store) DeletePlan(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin plan delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deletePlanTx(ctx, tx, path); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit plan delete for %s: %w", path, err)
	}
	return nil
}

func deletePlanTx(ctx context.Context, tx *sql.Tx, path string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete plan %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_fts WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete plan fts row %s: %w", path, err)
	}
	return nil
}

// PlanSearchQuery holds the parameters for a plan search.
type PlanSearchQuery struct {
	Query string
	Limit int
}

// PlanSearchResult is one match returned from SearchPlans.
type PlanSearchResult struct {
	Path        string
	Name        string
	Description string
	Snippet     string
}

// SearchPlans runs an FTS5 match against the plan index, ordered by relevance.
func (s *Store) SearchPlans(ctx context.Context, q PlanSearchQuery) ([]PlanSearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	matchExpr := sanitizeFTSQuery(q.Query)
	if matchExpr == "" {
		return nil, fmt.Errorf("store: search plans: empty query")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT path, name, description,
		       snippet(plan_fts, 3, '[', ']', '...', 12) AS snip
		FROM plan_fts
		WHERE plan_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search plans %q: %w", q.Query, err)
	}
	defer func() { _ = rows.Close() }()

	var results []PlanSearchResult
	for rows.Next() {
		var r PlanSearchResult
		if err := rows.Scan(&r.Path, &r.Name, &r.Description, &r.Snippet); err != nil {
			return nil, fmt.Errorf("store: scan plan search result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate plan search results: %w", err)
	}
	return results, nil
}

// PromptOffset returns the byte offset up to which path has already been
// parsed, and whether a row exists (a new file starts at offset 0).
func (s *Store) PromptOffset(ctx context.Context, path string) (int64, bool, error) {
	var offset int64
	err := s.db.QueryRowContext(ctx, `SELECT byte_offset FROM prompt_files WHERE path = ?`, path).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: query prompt offset for %s: %w", path, err)
	}
	return offset, true, nil
}

// InsertPromptMessages records that path has been parsed up to newOffset and
// inserts prompts (the prompts found in the newly-parsed span), all in one
// transaction. Prompts already indexed for path are skipped, so re-reading a
// span (a rewritten history file, or a concurrent scan) adds nothing.
func (s *Store) InsertPromptMessages(ctx context.Context, path string, newOffset, mtime int64, prompts []promptfile.Prompt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin prompt insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO prompt_files (path, byte_offset, mtime)
		VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET byte_offset = excluded.byte_offset, mtime = excluded.mtime
	`, path, newOffset, mtime); err != nil {
		return fmt.Errorf("store: upsert prompt offset for %s: %w", path, err)
	}

	for _, p := range prompts {
		if err := insertPromptMessage(ctx, tx, path, p); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit prompt insert for %s: %w", path, err)
	}
	return nil
}

func insertPromptMessage(ctx context.Context, tx *sql.Tx, path string, p promptfile.Prompt) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO prompt_messages (path, timestamp, session_id, text_hash)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, path, p.Timestamp, p.SessionID, textHash(p.Text))
	if err != nil {
		return fmt.Errorf("store: claim prompt message for %s: %w", path, err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: check prompt message claim for %s: %w", path, err)
	}
	if inserted == 0 {
		return nil // already indexed
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO prompt_fts (path, session_id, timestamp, project, text) VALUES (?, ?, ?, ?, ?)`,
		path, p.SessionID, p.Timestamp, p.Project, p.Text,
	); err != nil {
		return fmt.Errorf("store: insert prompt message for %s: %w", path, err)
	}
	return nil
}

// DeletePromptFile removes all indexed prompts for a history file.
func (s *Store) DeletePromptFile(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin prompt delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete prompt file %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_fts WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete prompt rows for %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_messages WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: delete prompt message keys for %s: %w", path, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit prompt delete for %s: %w", path, err)
	}
	return nil
}

// AllPromptPaths returns every indexed prompt file path.
func (s *Store) AllPromptPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM prompt_files`)
	if err != nil {
		return nil, fmt.Errorf("store: list prompt paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("store: scan prompt path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate prompt paths: %w", err)
	}
	return paths, nil
}

// PromptSearchQuery holds the parameters for a prompt-history search.
type PromptSearchQuery struct {
	Query   string
	Project string
	Limit   int
}

// PromptSearchResult is one match returned from SearchPrompts.
type PromptSearchResult struct {
	Path      string
	SessionID string
	Timestamp string
	Project   string
	Snippet   string
}

// SearchPrompts runs an FTS5 match against the global prompt history,
// optionally filtered by project, ordered by relevance.
func (s *Store) SearchPrompts(ctx context.Context, q PromptSearchQuery) ([]PromptSearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	matchExpr := sanitizeFTSQuery(q.Query)
	if matchExpr == "" {
		return nil, fmt.Errorf("store: search prompts: empty query")
	}
	if q.Project != "" {
		matchExpr = fmt.Sprintf("project:%s AND (%s)", quoteFTSTerm(q.Project), matchExpr)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT path, session_id, timestamp, project,
		       snippet(prompt_fts, 4, '[', ']', '...', 12) AS snip
		FROM prompt_fts
		WHERE prompt_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search prompts %q: %w", q.Query, err)
	}
	defer func() { _ = rows.Close() }()

	var results []PromptSearchResult
	for rows.Next() {
		var r PromptSearchResult
		if err := rows.Scan(&r.Path, &r.SessionID, &r.Timestamp, &r.Project, &r.Snippet); err != nil {
			return nil, fmt.Errorf("store: scan prompt search result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate prompt search results: %w", err)
	}
	return results, nil
}

// ToolCall is one recorded MCP tool invocation.
type ToolCall struct {
	Timestamp   time.Time
	Tool        string
	Query       string
	Project     string
	ResultCount int
	IsError     bool
	Duration    time.Duration
}

// RecordToolCall persists one MCP tool invocation.
func (s *Store) RecordToolCall(ctx context.Context, c ToolCall) error {
	isError := 0
	if c.IsError {
		isError = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls (ts, tool, query, project, result_count, is_error, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, c.Timestamp.UnixMilli(), c.Tool, c.Query, c.Project, c.ResultCount, isError, c.Duration.Milliseconds()); err != nil {
		return fmt.Errorf("store: record tool call %s: %w", c.Tool, err)
	}
	return nil
}

// ToolStat is one tool's contribution to ToolCallStats.
type ToolStat struct {
	Tool          string
	Calls         int
	Errors        int
	AvgDurationMs int64
	LastUsed      string
}

// QueryStat is one query's call count.
type QueryStat struct {
	Query string
	Calls int
}

// ToolCallStats summarizes recorded MCP usage.
type ToolCallStats struct {
	TotalCalls int
	Errors     int
	ByTool     []ToolStat
	TopQueries []QueryStat
	FirstCall  string
	LastCall   string
}

// ToolCallStats aggregates recorded MCP tool invocations. limit bounds
// TopQueries (0 uses 10).
func (s *Store) ToolCallStats(ctx context.Context, limit int) (ToolCallStats, error) {
	if limit <= 0 {
		limit = 10
	}
	var stats ToolCallStats

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(is_error), 0) FROM tool_calls
	`).Scan(&stats.TotalCalls, &stats.Errors); err != nil {
		return ToolCallStats{}, fmt.Errorf("store: count tool calls: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT tool, COUNT(*), COALESCE(SUM(is_error), 0),
		       CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER), MAX(ts)
		FROM tool_calls
		GROUP BY tool
		ORDER BY COUNT(*) DESC, tool
	`)
	if err != nil {
		return ToolCallStats{}, fmt.Errorf("store: tool call stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st ToolStat
		var lastMs int64
		if err := rows.Scan(&st.Tool, &st.Calls, &st.Errors, &st.AvgDurationMs, &lastMs); err != nil {
			return ToolCallStats{}, fmt.Errorf("store: scan tool stat: %w", err)
		}
		st.LastUsed = formatMillis(lastMs)
		stats.ByTool = append(stats.ByTool, st)
	}
	if err := rows.Err(); err != nil {
		return ToolCallStats{}, fmt.Errorf("store: iterate tool stats: %w", err)
	}

	queryRows, err := s.db.QueryContext(ctx, `
		SELECT query, COUNT(*) FROM tool_calls
		WHERE query != ''
		GROUP BY query
		ORDER BY COUNT(*) DESC, query
		LIMIT ?
	`, limit)
	if err != nil {
		return ToolCallStats{}, fmt.Errorf("store: top queries: %w", err)
	}
	defer func() { _ = queryRows.Close() }()
	for queryRows.Next() {
		var qs QueryStat
		if err := queryRows.Scan(&qs.Query, &qs.Calls); err != nil {
			return ToolCallStats{}, fmt.Errorf("store: scan query stat: %w", err)
		}
		stats.TopQueries = append(stats.TopQueries, qs)
	}
	if err := queryRows.Err(); err != nil {
		return ToolCallStats{}, fmt.Errorf("store: iterate query stats: %w", err)
	}

	var first, last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM tool_calls`).Scan(&first, &last); err != nil {
		return ToolCallStats{}, fmt.Errorf("store: query tool call range: %w", err)
	}
	stats.FirstCall = formatNullableMillis(first)
	stats.LastCall = formatNullableMillis(last)

	return stats, nil
}

func formatMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func formatNullableMillis(n sql.NullInt64) string {
	if !n.Valid {
		return ""
	}
	return formatMillis(n.Int64)
}

// ResetReport is one table's contribution to a reset: its name and how many
// rows it held before the reset cleared it.
type ResetReport struct {
	Table string
	Rows  int64
}

// ResetMemory clears the memory index: the file records and their FTS rows.
// The markdown files are untouched, so a scan afterwards rebuilds it.
func (s *Store) ResetMemory(ctx context.Context) ([]ResetReport, error) {
	return s.reset(ctx, "files", "memory_fts")
}

// ResetHistory clears the conversation-history index. transcript_files holds
// each transcript's byte offset, so clearing it makes the next scan re-read
// every transcript from the start. history_messages holds the message
// identities that gate inserts (see insertHistoryMessage), so it has to go
// too: leave it behind and every message would still count as already
// indexed, and the re-index would silently do nothing.
func (s *Store) ResetHistory(ctx context.Context) ([]ResetReport, error) {
	return s.reset(ctx, "transcript_files", "history_fts", "history_messages")
}

// ResetPlans clears the plan index. The markdown files are untouched, so a
// scan afterwards rebuilds it.
func (s *Store) ResetPlans(ctx context.Context) ([]ResetReport, error) {
	return s.reset(ctx, "plan_files", "plan_fts")
}

// ResetPrompts clears the prompt-history index. prompt_files holds the byte
// offset and prompt_messages holds the dedup identities, so both have to go
// for the next scan to re-read and re-index the file.
func (s *Store) ResetPrompts(ctx context.Context) ([]ResetReport, error) {
	return s.reset(ctx, "prompt_files", "prompt_fts", "prompt_messages")
}

// Vacuum rewrites the database file, returning the space a reset freed to the
// filesystem. Deleting rows leaves the file at its high-water mark, so a reset
// on a large index reclaims nothing until this runs. SQLite refuses to VACUUM
// inside a transaction, so it runs on its own.
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("store: vacuum: %w", err)
	}
	return nil
}

// reset empties tables in one transaction, reporting what each held. The
// table names are constants from this package, never caller input.
func (s *Store) reset(ctx context.Context, tables ...string) ([]ResetReport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin reset tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	reports := make([]ResetReport, 0, len(tables))
	for _, table := range tables {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+table)
		if err != nil {
			return nil, fmt.Errorf("store: clear %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("store: count rows cleared from %s: %w", table, err)
		}
		reports = append(reports, ResetReport{Table: table, Rows: n})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit reset: %w", err)
	}
	return reports, nil
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
	PlanFiles       int
	PromptMessages  int
	PromptSessions  int
	PromptProjects  int
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

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_files`).Scan(&stats.PlanFiles); err != nil {
		return Stats{}, fmt.Errorf("store: count plan files: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT session_id), COUNT(DISTINCT project) FROM prompt_fts
	`).Scan(&stats.PromptMessages, &stats.PromptSessions, &stats.PromptProjects); err != nil {
		return Stats{}, fmt.Errorf("store: count prompts: %w", err)
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
