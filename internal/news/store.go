package news

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS news_articles (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, symbols_json TEXT NOT NULL, title TEXT NOT NULL, summary TEXT NOT NULL, url TEXT NOT NULL, image_url TEXT NOT NULL, publisher TEXT NOT NULL, published_at INTEGER NOT NULL, received_at INTEGER NOT NULL, provider TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS news_published ON news_articles(published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS news_received ON news_articles(received_at)`,
		`CREATE TABLE IF NOT EXISTS news_symbols (article_id TEXT NOT NULL, symbol TEXT NOT NULL, PRIMARY KEY(article_id,symbol))`,
		`CREATE INDEX IF NOT EXISTS news_symbols_symbol ON news_symbols(symbol,article_id)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Exists(ctx context.Context, id string) bool {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1 FROM news_articles WHERE id=?`, id).Scan(&one) == nil
}

func (s *Store) Insert(ctx context.Context, a Article) (Article, bool, error) {
	return s.insert(ctx, a, false)
}

func (s *Store) InsertRemote(ctx context.Context, a Article) (Article, bool, error) {
	return s.insert(ctx, a, true)
}

func (s *Store) insert(ctx context.Context, a Article, preserveSequence bool) (Article, bool, error) {
	symbols, _ := json.Marshal(a.Symbols)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, false, err
	}
	defer tx.Rollback()
	var result sql.Result
	if preserveSequence && a.Sequence > 0 {
		result, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO news_articles(sequence,id,kind,symbols_json,title,summary,url,image_url,publisher,published_at,received_at,provider) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, a.Sequence, a.ID, a.Kind, string(symbols), a.Title, a.Summary, a.URL, a.ImageURL, a.Publisher, a.PublishedAt.UnixMilli(), a.ReceivedAt.UnixMilli(), a.Provider)
	} else {
		result, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO news_articles(id,kind,symbols_json,title,summary,url,image_url,publisher,published_at,received_at,provider) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Kind, string(symbols), a.Title, a.Summary, a.URL, a.ImageURL, a.Publisher, a.PublishedAt.UnixMilli(), a.ReceivedAt.UnixMilli(), a.Provider)
	}
	if err != nil {
		return a, false, err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		if a.Kind == PressRelease {
			if _, err := tx.ExecContext(ctx, `UPDATE news_articles SET kind=? WHERE id=?`, PressRelease, a.ID); err != nil {
				return a, false, err
			}
		}
		for _, symbol := range a.Symbols {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO news_symbols(article_id,symbol) VALUES(?,?)`, a.ID, symbol); err != nil {
				return a, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return a, false, err
		}
		return a, false, nil
	}
	if !preserveSequence || a.Sequence == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT sequence FROM news_articles WHERE id=?`, a.ID).Scan(&a.Sequence); err != nil {
			return a, false, err
		}
	}
	for _, symbol := range a.Symbols {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO news_symbols(article_id,symbol) VALUES(?,?)`, a.ID, symbol); err != nil {
			return a, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return a, false, err
	}
	return a, true, nil
}

func (s *Store) LatestSequence(ctx context.Context) int64 {
	var sequence int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM news_articles`).Scan(&sequence)
	return sequence
}

func (s *Store) List(ctx context.Context, q Query) ([]Article, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	where := []string{"1=1"}
	var args []any
	if q.AfterSequence > 0 {
		where = append(where, "a.sequence>?")
		args = append(args, q.AfterSequence)
	}
	if q.BeforeSequence > 0 {
		where = append(where, "a.sequence<?")
		args = append(args, q.BeforeSequence)
	}
	if q.UntilSequence > 0 {
		where = append(where, "a.sequence<=?")
		args = append(args, q.UntilSequence)
	}
	if len(q.Kinds) > 0 {
		parts := make([]string, len(q.Kinds))
		for i, kind := range q.Kinds {
			parts[i] = "?"
			args = append(args, kind)
		}
		where = append(where, "a.kind IN ("+strings.Join(parts, ",")+")")
	}
	if len(q.Symbols) > 0 {
		parts := make([]string, len(q.Symbols))
		for i, symbol := range q.Symbols {
			parts[i] = "?"
			args = append(args, symbol)
		}
		where = append(where, "EXISTS (SELECT 1 FROM news_symbols s WHERE s.article_id=a.id AND s.symbol IN ("+strings.Join(parts, ",")+"))")
	}
	direction := "DESC"
	if q.AfterSequence > 0 {
		direction = "ASC"
	}
	args = append(args, q.Limit)
	query := `SELECT a.sequence,a.id,a.kind,a.symbols_json,a.title,a.summary,a.url,a.image_url,a.publisher,a.published_at,a.received_at,a.provider FROM news_articles a WHERE ` + strings.Join(where, " AND ") + ` ORDER BY a.sequence ` + direction + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Article, 0)
	for rows.Next() {
		var a Article
		var symbols string
		var published, received int64
		if err := rows.Scan(&a.Sequence, &a.ID, &a.Kind, &symbols, &a.Title, &a.Summary, &a.URL, &a.ImageURL, &a.Publisher, &published, &received, &a.Provider); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(symbols), &a.Symbols)
		a.PublishedAt = time.UnixMilli(published).UTC()
		a.ReceivedAt = time.UnixMilli(received).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM news_symbols WHERE article_id IN (SELECT id FROM news_articles WHERE received_at<?)`, before.UnixMilli()); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM news_articles WHERE received_at<?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return n, err
}
