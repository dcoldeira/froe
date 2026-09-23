// Package session persists conversations, run metrics and project memory.
//
// SQLite via modernc.org/sqlite: a pure-Go driver, so there is no cgo and the
// single static binary and trivial cross-compilation from D1 survive.
package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the on-disk database.
type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  project    TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project, updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq          INTEGER NOT NULL,
  role         TEXT NOT NULL,
  content      TEXT NOT NULL DEFAULT '',
  tool_calls   TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq);

CREATE TABLE IF NOT EXISTS runs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id        TEXT NOT NULL,
  model             TEXT NOT NULL DEFAULT '',
  turns             INTEGER NOT NULL DEFAULT 0,
  tool_calls        INTEGER NOT NULL DEFAULT 0,
  tool_errors       INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
  elapsed_ms        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memories (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project    TEXT NOT NULL,
  text       TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT 'agent',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project, created_at DESC);
`

// DefaultPath is where the database lives, honouring XDG_DATA_HOME.
func DefaultPath() string {
	if p := os.Getenv("FROE_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "froe.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "froe", "froe.db")
}

// Open creates or opens the store.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// WAL keeps a long-running chat from blocking a concurrent `froe do`,
	// and busy_timeout turns a transient lock into a wait rather than an error.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

// Session is one conversation.
type Session struct {
	ID        string
	Project   string
	Title     string
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  int
}

// NewSession starts a conversation. The id is time-ordered so that listing by
// id and by recency agree.
func (s *Store) NewSession(project, title, model string) (*Session, error) {
	now := time.Now()
	id := fmt.Sprintf("%d-%04d", now.UnixMilli(), now.Nanosecond()%10000)
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project, title, model, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		id, project, truncate(title, 120), model, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Project: project, Title: title, Model: model, CreatedAt: now, UpdatedAt: now}, nil
}

// SetTitle names a session after the fact, so a chat started blank still shows
// something meaningful in `froe sessions`.
func (s *Store) SetTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE id = ? AND title = ''`,
		truncate(title, 120), id)
	return err
}

// Latest returns the most recently updated session for a project.
func (s *Store) Latest(project string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, project, title, model, created_at, updated_at
		FROM sessions WHERE project = ? ORDER BY updated_at DESC LIMIT 1`, project)
	return scanSession(row)
}

// Get returns one session by id.
func (s *Store) Get(id string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, project, title, model, created_at, updated_at
		FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// List returns recent sessions, newest first. An empty project lists all.
func (s *Store) List(project string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT s.id, s.project, s.title, s.model, s.created_at, s.updated_at,
	             (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)
	      FROM sessions s`
	args := []any{}
	if project != "" {
		q += ` WHERE s.project = ?`
		args = append(args, project)
	}
	q += ` ORDER BY s.updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		var c, u int64
		if err := rows.Scan(&s.ID, &s.Project, &s.Title, &s.Model, &c, &u, &s.Messages); err != nil {
			return nil, err
		}
		s.CreatedAt, s.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// StoredMessage is a persisted conversation turn.
type StoredMessage struct {
	Role       string
	Content    string
	ToolCalls  string // raw JSON, empty when none
	ToolCallID string
}

// AppendMessages saves turns and bumps the session's updated_at.
func (s *Store) AppendMessages(sessionID string, msgs []StoredMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), -1) + 1 FROM messages WHERE session_id = ?`,
		sessionID).Scan(&next); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO messages
		(session_id, seq, role, content, tool_calls, tool_call_id, created_at)
		VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for i, m := range msgs {
		if _, err := stmt.Exec(sessionID, next+i, m.Role, m.Content, m.ToolCalls, m.ToolCallID, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// Messages returns a session's turns in order.
func (s *Store) Messages(sessionID string) ([]StoredMessage, error) {
	rows, err := s.db.Query(`
		SELECT role, content, tool_calls, tool_call_id
		FROM messages WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(&m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RunRecord is the metrics for one agent run.
type RunRecord struct {
	SessionID        string
	Model            string
	Turns            int
	ToolCalls        int
	ToolErrors       int
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	Elapsed          time.Duration
}

// SaveRun records run metrics. This is the corpus `froe bench` aggregates
// (D9): real numbers from real work, not a synthetic benchmark.
func (s *Store) SaveRun(r RunRecord) error {
	_, err := s.db.Exec(`INSERT INTO runs
		(session_id, model, turns, tool_calls, tool_errors,
		 prompt_tokens, completion_tokens, reasoning_tokens, elapsed_ms, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Model, r.Turns, r.ToolCalls, r.ToolErrors,
		r.PromptTokens, r.CompletionTokens, r.ReasoningTokens,
		r.Elapsed.Milliseconds(), time.Now().Unix())
	return err
}

func scanSession(row *sql.Row) (*Session, error) {
	var s Session
	var c, u int64
	if err := row.Scan(&s.ID, &s.Project, &s.Title, &s.Model, &c, &u); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
	return &s, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// EncodeToolCalls serialises tool calls for storage.
func EncodeToolCalls(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if string(b) == "null" {
		return ""
	}
	return string(b)
}

// Memory is one durable fact about a project.
type Memory struct {
	ID        int64
	Project   string
	Text      string
	Source    string
	CreatedAt time.Time
}

// maxMemoriesInContext bounds what is injected into a prompt. Memory that
// crowds out the task defeats its own purpose.
const maxMemoriesInContext = 30

// Remember stores a durable fact, ignoring exact duplicates.
//
// Deduplication matters because the agent writes these itself: without it, a
// model that re-learns the same thing each session accumulates a prompt full of
// the same sentence.
func (s *Store) Remember(project, text, source string) error {
	text = trimSpace(text)
	if text == "" {
		return fmt.Errorf("empty memory")
	}
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM memories WHERE project = ? AND text = ?`, project, text).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO memories (project, text, source, created_at) VALUES (?,?,?,?)`,
		project, text, source, time.Now().Unix())
	return err
}

// Memories returns a project's facts, newest first.
func (s *Store) Memories(project string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = maxMemoriesInContext
	}
	rows, err := s.db.Query(
		`SELECT id, project, text, source, created_at FROM memories
		 WHERE project = ? ORDER BY created_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		var c int64
		if err := rows.Scan(&m.ID, &m.Project, &m.Text, &m.Source, &c); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(c, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Forget deletes one memory by id.
func (s *Store) Forget(id int64) error {
	res, err := s.db.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no memory with id %d", id)
	}
	return nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
