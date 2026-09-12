// Package importindex stores rebuildable provider metadata, never transcript bodies.
package importindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

var ErrInvalidQuery = errors.New("invalid search query")

type Index struct{ db *sql.DB }
type Result struct {
	ID      string
	Session sessionimport.ImportableSession
}

func ID(provider, root, native string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + root + "\x00" + native))
	return hex.EncodeToString(sum[:])
}
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "session-search-v1.db")}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = db.Close()
		return nil, err
	}
	if version != 0 && version != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("unsupported session search cache version %d", version)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS files(root TEXT NOT NULL,path TEXT NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,generation TEXT NOT NULL,id TEXT NOT NULL,activity INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(root,path));
 CREATE INDEX IF NOT EXISTS files_id ON files(id,activity DESC,path);
 CREATE TABLE IF NOT EXISTS seen_titles(root TEXT NOT NULL,native TEXT NOT NULL,PRIMARY KEY(root,native));
 CREATE TABLE IF NOT EXISTS scan_state(root TEXT PRIMARY KEY,incomplete INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS title_fingerprints(root TEXT PRIMARY KEY,size INTEGER NOT NULL,mtime INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS titles(root TEXT NOT NULL,native TEXT NOT NULL,title TEXT NOT NULL,PRIMARY KEY(root,native));
 CREATE TABLE IF NOT EXISTS results(id TEXT PRIMARY KEY,root TEXT NOT NULL,native TEXT NOT NULL,title TEXT NOT NULL,normalized TEXT NOT NULL,activity INTEGER NOT NULL,data BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS results_recent ON results(activity DESC,id);
 CREATE INDEX IF NOT EXISTS results_title ON results(normalized);
 CREATE TABLE IF NOT EXISTS grams(gram TEXT NOT NULL,id TEXT NOT NULL,PRIMARY KEY(gram,id));`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(`PRAGMA user_version=1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}
func (i *Index) Close() error { return i.db.Close() }

// Normalize folds compatibility Unicode and whitespace while preserving readable letters.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(s))), " ")
}
func grams(s string) []string {
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(word)
		for n := 0; n+2 < len(r); n++ {
			seen[string(r[n:n+3])] = true
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}
func (i *Index) Seen(ctx context.Context, root, path string, size, mtime int64, generation string) (bool, error) {
	result, err := i.db.ExecContext(ctx, `UPDATE files SET generation=? WHERE root=? AND path=? AND size=? AND mtime=?`, generation, root, path, size, mtime)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
func (i *Index) Put(ctx context.Context, root, path string, size, mtime int64, generation string, s sessionimport.ImportableSession) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	id := ID(string(s.Provider), root, s.NativeSessionID)
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT id FROM files WHERE root=? AND path=?`, root, path).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO files VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(root,path) DO UPDATE SET size=excluded.size,mtime=excluded.mtime,generation=excluded.generation,id=excluded.id,activity=excluded.activity,data=excluded.data`, root, path, size, mtime, generation, id, s.LastActivity.UnixNano(), data)
	if err != nil {
		return err
	}
	if err = rebuild(ctx, tx, id); err != nil {
		return err
	}
	if previous != "" && previous != id {
		if err = rebuild(ctx, tx, previous); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func rebuild(ctx context.Context, tx *sql.Tx, id string) error {
	var data []byte
	var root string
	err := tx.QueryRowContext(ctx, `SELECT root,data FROM files WHERE id=? ORDER BY activity DESC,path LIMIT 1`, id).Scan(&root, &data)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `DELETE FROM results WHERE id=?`, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM grams WHERE id=?`, id)
		}
		return err
	}
	if err != nil {
		return err
	}
	var s sessionimport.ImportableSession
	if err = json.Unmarshal(data, &s); err != nil {
		return err
	}
	var title string
	err = tx.QueryRowContext(ctx, `SELECT title FROM titles WHERE root=? AND native=?`, root, s.NativeSessionID).Scan(&title)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if title != "" {
		s.Title = title
	}
	data, err = json.Marshal(s)
	if err != nil {
		return err
	}
	normalized := Normalize(s.Title)
	_, err = tx.ExecContext(ctx, `INSERT INTO results VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET root=excluded.root,native=excluded.native,title=excluded.title,normalized=excluded.normalized,activity=excluded.activity,data=excluded.data`, id, root, s.NativeSessionID, s.Title, normalized, s.LastActivity.UnixNano(), data)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM grams WHERE id=?`, id); err != nil {
		return err
	}
	for _, g := range grams(normalized) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO grams VALUES(?,?)`, g, id); err != nil {
			return err
		}
	}
	return nil
}
func (i *Index) Title(ctx context.Context, root, native, title string) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO titles VALUES(?,?,?) ON CONFLICT(root,native) DO UPDATE SET title=excluded.title`, root, native, title)
	if err != nil {
		return err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM results WHERE root=? AND native=? LIMIT 1`, root, native).Scan(&id)
	if err == nil {
		err = rebuild(ctx, tx, id)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return tx.Commit()
}

// Complete deletes stale rows only after the caller reports an error-free traversal.
func (i *Index) Complete(ctx context.Context, root, generation string) error {
	for {
		rows, err := i.db.QueryContext(ctx, `SELECT DISTINCT id FROM files WHERE root=? AND generation!=? LIMIT 64`, root, generation)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		tx, err := i.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, id := range ids {
			_, err = tx.ExecContext(ctx, `DELETE FROM files WHERE root=? AND generation!=? AND id=?`, root, generation, id)
			if err == nil {
				err = rebuild(ctx, tx, id)
			}
			if err != nil {
				break
			}
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}
func (i *Index) Get(ctx context.Context, id string) (Result, error) {
	var data []byte
	err := i.db.QueryRowContext(ctx, `SELECT data FROM results WHERE id=?`, id).Scan(&data)
	r := Result{ID: id}
	if err == nil {
		err = json.Unmarshal(data, &r.Session)
	}
	return r, err
}

// Search retrieves at most 1000 candidates from SQLite; the Go heap is independent of history size.
// Exact, phrase and word matches are fetched ahead of the bounded fuzzy pool.
func (i *Index) Search(ctx context.Context, query string, limit, offset int) ([]Result, bool, error) {
	q := Normalize(query)
	if len([]rune(q)) > 120 {
		return nil, false, fmt.Errorf("%w: query exceeds 120 characters", ErrInvalidQuery)
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	if offset < 0 || offset > 1000000 {
		return nil, false, fmt.Errorf("%w: invalid cursor", ErrInvalidQuery)
	}
	type candidate struct {
		result Result
		rank   int
	}
	candidates := []candidate{}
	seen := map[string]bool{}
	read := func(rows *sql.Rows, ranked bool) error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			var data []byte
			if err := rows.Scan(&id, &data); err != nil {
				return err
			}
			if seen[id] {
				continue
			}
			var s sessionimport.ImportableSession
			if err := json.Unmarshal(data, &s); err != nil {
				return err
			}
			rank := 0
			if ranked {
				rank = relevance(Normalize(s.Title), q)
				if rank < 0 {
					continue
				}
			}
			seen[id] = true
			candidates = append(candidates, candidate{Result{id, s}, rank})
		}
		return rows.Err()
	}
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = i.db.QueryContext(ctx, `SELECT id,data FROM results ORDER BY activity DESC,id LIMIT ? OFFSET ?`, limit+1, offset)
		if err != nil {
			return nil, false, err
		}
		if err = read(rows, false); err != nil {
			return nil, false, err
		}
		offset = 0
	} else {
		words := strings.Fields(q)
		conditions := []string{}
		args := []any{q, q, q}
		for _, w := range words {
			conditions = append(conditions, "instr(normalized,?)>0")
			args = append(args, w)
		}
		rows, err = i.db.QueryContext(ctx, `SELECT id,data FROM results WHERE `+strings.Join(conditions, " AND ")+` ORDER BY CASE WHEN normalized=? THEN 0 WHEN instr(normalized,?)=1 THEN 1 WHEN instr(normalized,?)>0 THEN 2 ELSE 3 END,activity DESC,id LIMIT 1000`, append(args[3:], args[:3]...)...)
		if err != nil {
			return nil, false, err
		}
		if err = read(rows, true); err != nil {
			return nil, false, err
		}
		gs := grams(q)
		if len(gs) > 0 && len(candidates) < 1000 {
			marks := make([]string, len(gs))
			ga := make([]any, 0, len(gs)+1)
			for n, g := range gs {
				marks[n] = "?"
				ga = append(ga, g)
			}
			ga = append(ga, 1000-len(candidates))
			rows, err = i.db.QueryContext(ctx, `SELECT r.id,r.data FROM results r JOIN (SELECT id,count(*) hits FROM grams WHERE gram IN (`+strings.Join(marks, ",")+`) GROUP BY id ORDER BY hits DESC,id LIMIT ?) g ON g.id=r.id ORDER BY g.hits DESC,r.activity DESC,r.id`, ga...)
			if err != nil {
				return nil, false, err
			}
			if err = read(rows, true); err != nil {
				return nil, false, err
			}
		}
	}
	sort.Slice(candidates, func(a, b int) bool {
		x, y := candidates[a], candidates[b]
		if x.rank != y.rank {
			return x.rank < y.rank
		}
		if !x.result.Session.LastActivity.Equal(y.result.Session.LastActivity) {
			return x.result.Session.LastActivity.After(y.result.Session.LastActivity)
		}
		return x.result.ID < y.result.ID
	})
	start := min(offset, len(candidates))
	end := min(start+limit, len(candidates))
	out := make([]Result, 0, end-start)
	for _, c := range candidates[start:end] {
		out = append(out, c.result)
	}
	return out, end < len(candidates), nil
}
func relevance(title, q string) int {
	if title == q {
		return 0
	}
	if strings.HasPrefix(title, q) {
		return 1
	}
	if strings.Contains(title, q) {
		return 2
	}
	all := true
	for _, w := range strings.Fields(q) {
		if !strings.Contains(title, w) {
			all = false
		}
	}
	if all {
		return 3
	}
	for _, wanted := range strings.Fields(q) {
		found := false
		for _, word := range strings.Fields(title) {
			if strings.Contains(word, wanted) || oneEdit(word, wanted) {
				found = true
				break
			}
		}
		if !found {
			return -1
		}
	}
	return 4
}
func oneEdit(a, b string) bool {
	x, y := []rune(a), []rune(b)
	if len(y) < 4 || len(x)-len(y) > 1 || len(y)-len(x) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(x) && j < len(y) {
		if x[i] == y[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(x) == len(y) {
			if i+1 < len(x) && x[i] == y[j+1] && x[i+1] == y[j] {
				i += 2
				j += 2
			} else {
				i++
				j++
			}
		} else if len(x) > len(y) {
			i++
		} else {
			j++
		}
	}
	if i < len(x) || j < len(y) {
		edits++
	}
	return edits <= 1
}

// TitlesUnchanged avoids rereading and rewriting the provider title index on warm refreshes.
func (i *Index) TitlesUnchanged(ctx context.Context, root string, size, mtime int64) (bool, error) {
	var found int
	err := i.db.QueryRowContext(ctx, `SELECT 1 FROM title_fingerprints WHERE root=? AND size=? AND mtime=?`, root, size, mtime).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return found == 1, err
}
func (i *Index) MarkTitles(ctx context.Context, root string, size, mtime int64) error {
	_, err := i.db.ExecContext(ctx, `INSERT INTO title_fingerprints VALUES(?,?,?) ON CONFLICT(root) DO UPDATE SET size=excluded.size,mtime=excluded.mtime`, root, size, mtime)
	return err
}

func (i *Index) MarkScan(ctx context.Context, root string, incomplete bool) error {
	_, err := i.db.ExecContext(ctx, `INSERT INTO scan_state VALUES(?,?) ON CONFLICT(root) DO UPDATE SET incomplete=excluded.incomplete`, root, incomplete)
	return err
}
func (i *Index) Incomplete(ctx context.Context) (bool, error) {
	var n int
	err := i.db.QueryRowContext(ctx, `SELECT count(*) FROM scan_state WHERE incomplete=1`).Scan(&n)
	return n > 0, err
}

func (i *Index) BeginTitles(ctx context.Context, root string) error {
	_, err := i.db.ExecContext(ctx, `DELETE FROM seen_titles WHERE root=?`, root)
	return err
}
func (i *Index) SeenTitle(ctx context.Context, root, native string) error {
	_, err := i.db.ExecContext(ctx, `INSERT OR IGNORE INTO seen_titles VALUES(?,?)`, root, native)
	return err
}
func (i *Index) CompleteTitles(ctx context.Context, root string) error {
	for {
		var native string
		err := i.db.QueryRowContext(ctx, `SELECT native FROM titles t WHERE root=? AND NOT EXISTS(SELECT 1 FROM seen_titles s WHERE s.root=t.root AND s.native=t.native) LIMIT 1`, root).Scan(&native)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		tx, err := i.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM titles WHERE root=? AND native=?`, root, native)
		if err == nil {
			var id string
			err = tx.QueryRowContext(ctx, `SELECT id FROM results WHERE root=? AND native=? LIMIT 1`, root, native).Scan(&id)
			if err == nil {
				err = rebuild(ctx, tx, id)
			} else if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}
