package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/broker/wal"
	_ "modernc.org/sqlite"
)

var ErrIndexStale = errors.New("artifact index is stale or rebuilding")

// Index is a disposable FTS5 projection. It never decides scope, sensitivity,
// or authority; Search rechecks every hit against the canonical WAL projection.
type Index struct {
	path string
	db   *sql.DB
}

func OpenIndex(path string) (*Index, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("artifact index path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Index{path: path, db: db}, nil
}

func (i *Index) Close() error { return i.db.Close() }

// Rebuild writes a complete temporary database, then atomically publishes it.
// The current connection is replaced only after the new projection commits.
func (i *Index) Rebuild(records []wal.Record) error {
	items, err := fold(records)
	if err != nil {
		return err
	}
	tmp := i.path + ".rebuild"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", tmp)
	if err != nil {
		return err
	}
	fail := func(e error) error { _ = db.Close(); _ = os.Remove(tmp); return e }
	if _, err = db.Exec(`PRAGMA journal_mode=DELETE;
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE artifacts(id TEXT PRIMARY KEY, version INTEGER NOT NULL, authority TEXT NOT NULL, sensitivity TEXT NOT NULL);
CREATE VIRTUAL TABLE artifact_fts USING fts5(id UNINDEXED, summary, content, trigger, tags, groups);`); err != nil {
		return fail(err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	for _, a := range items {
		if _, err = tx.Exec(`INSERT INTO artifacts(id,version,authority,sensitivity) VALUES(?,?,?,?)`, a.ID, a.Version, a.Authority, a.Sensitivity); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		// Normal content only. Private/secret metadata and bodies do not enter
		// the ordinary FTS corpus.
		if a.Sensitivity == "normal" && a.Authority != AuthorityDeleted {
			if _, err = tx.Exec(`INSERT INTO artifact_fts(id,summary,content,trigger,tags,groups) VALUES(?,?,?,?,?,?)`, a.ID, a.Summary, a.Content, a.Trigger, strings.Join(a.Tags, " "), strings.Join(a.Groups, " ")); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
	}
	seq, digest := checkpoint(records)
	if _, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('sequence',?),('digest',?)`, fmt.Sprint(seq), digest); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	if _, err = db.Exec(`PRAGMA optimize`); err != nil {
		return fail(err)
	}
	if err = db.Close(); err != nil {
		return fail(err)
	}
	if err = os.Chmod(tmp, 0o600); err != nil {
		return fail(err)
	}
	if err = i.db.Close(); err != nil {
		return fail(err)
	}
	if err = os.Rename(tmp, i.path); err != nil {
		return err
	}
	i.db, err = sql.Open("sqlite", i.path)
	return err
}

// Search returns canonical artifacts, never raw index rows. Callers therefore
// cannot use stale/private index metadata to bypass projection authorization.
func (i *Index) Search(ctx context.Context, svc *Service, text string, q Query) ([]Artifact, error) {
	records := svc.wal.Records()
	seq, digest := checkpoint(records)
	var gotSeq, gotDigest string
	if err := i.db.QueryRowContext(ctx, `SELECT
COALESCE((SELECT value FROM meta WHERE key='sequence'),''),
COALESCE((SELECT value FROM meta WHERE key='digest'),'')`).Scan(&gotSeq, &gotDigest); err != nil || gotSeq != fmt.Sprint(seq) || gotDigest != digest {
		return nil, ErrIndexStale
	}
	rows, err := i.db.QueryContext(ctx, `SELECT id FROM artifact_fts WHERE artifact_fts MATCH ? ORDER BY rank LIMIT ?`, text, queryLimit(q.MaxItems))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	allowed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	canonical, err := svc.Query(q)
	if err != nil {
		return nil, err
	}
	out := canonical[:0]
	for _, a := range canonical {
		if allowed[a.ID] {
			out = append(out, a)
		}
	}
	return out, nil
}

func queryLimit(n int) int {
	if n <= 0 {
		return 50
	}
	return n
}
func checkpoint(records []wal.Record) (uint64, string) {
	for i := len(records) - 1; i >= 0; i-- {
		for _, event := range records[i].Transaction.Events {
			if event.Store == artifactStore && (event.Type == "artifact.create" || event.Type == "artifact.edit" || event.Type == "artifact.authority") {
				return records[i].Sequence, records[i].Digest
			}
		}
	}
	return 0, ""
}
