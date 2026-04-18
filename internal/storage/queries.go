package storage

import (
	"context"
)

type Session struct {
	ID        string
	Provider  string
	Model     string
	Workdir   string
}

func (db *DB) GetSession(ctx context.Context, id string) (*Session, error) {
	var s Session
	err := db.QueryRowContext(ctx, "SELECT id, provider, model, workdir FROM sessions WHERE id = ?", id).
		Scan(&s.ID, &s.Provider, &s.Model, &s.Workdir)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

type Message struct {
	ID          string
	Role        string
	ContentJSON string
}

func (db *DB) GetMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, role, content_json FROM messages WHERE session_id = ? ORDER BY seq ASC", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.ContentJSON); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (db *DB) GetLatestSessionID(ctx context.Context) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, "SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1").Scan(&id)
	return id, err
}

func (db *DB) UpdateSessionTimestamp(ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx, "UPDATE sessions SET updated_at = strftime('%s','now') WHERE id = ?", id)
	return err
}
