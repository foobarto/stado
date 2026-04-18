package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  title TEXT,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  workdir TEXT NOT NULL,
  parent_session_id TEXT
);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content_json TEXT NOT NULL,
  tokens_in INTEGER,
  tokens_out INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_session_seq ON messages(session_id, seq);

CREATE TABLE IF NOT EXISTS tool_calls (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  args_json TEXT NOT NULL,
  result_json TEXT,
  status TEXT NOT NULL,
  started_at INTEGER,
  finished_at INTEGER
);

CREATE TABLE IF NOT EXISTS todos (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  status TEXT NOT NULL,
  priority TEXT NOT NULL,
  position INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  session_id TEXT,
  kind TEXT NOT NULL,
  detail_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS indexed_files (
  path TEXT PRIMARY KEY,
  mtime INTEGER NOT NULL,
  size INTEGER NOT NULL,
  hash TEXT NOT NULL
);
`

type DB struct {
	*sql.DB
}

func Open(stateDir string) (*DB, error) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	dbPath := filepath.Join(stateDir, "stado.db")

	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &DB{conn}, nil
}

func (db *DB) Close() error {
	return db.DB.Close()
}

func (db *DB) CreateSession(ctx context.Context, id, provider, model, workdir string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at, provider, model, workdir)
		 VALUES (?, strftime('%s','now'), strftime('%s','now'), ?, ?, ?)`,
		id, provider, model, workdir)
	return err
}

func (db *DB) AppendMessage(ctx context.Context, sessionID, id, role, contentJSON string, seq int) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, seq, role, content_json, created_at)
		 VALUES (?, ?, ?, ?, ?, strftime('%s','now'))`,
		id, sessionID, seq, role, contentJSON)
	if err == nil {
		db.UpdateSessionTimestamp(ctx, sessionID)
	}
	return err
}
