package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

func fold(s string) string { return cases.Fold().String(norm.NFC.String(s)) }
func natural(s string) string {
	r := []rune(fold(s))
	var b strings.Builder
	for i := 0; i < len(r); {
		if r[i] >= '0' && r[i] <= '9' {
			j := i
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				j++
			}
			n := strings.TrimLeft(string(r[i:j]), "0")
			fmt.Fprintf(&b, "\x01%08d%s\x02", len(n), n)
			i = j
		} else {
			b.WriteRune(r[i])
			i++
		}
	}
	return b.String()
}

type Store struct{ db *sql.DB }
type Photo struct {
	ID          int64    `json:"id"`
	Path        string   `json:"path"`
	Folder      string   `json:"folder"`
	Name        string   `json:"name"`
	Size        int64    `json:"size"`
	Mtime       int64    `json:"mtime"`
	Taken       int64    `json:"taken"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Orientation int      `json:"orientation"`
	Tags        []string `json:"tags"`
	Error       string   `json:"error,omitempty"`
}
type Folder struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Cover int64  `json:"cover"`
}
type Query struct {
	Folder, Search, Scope, Sort, Tag string
	Page, Limit                      int
	ExactTags                        []string
}
type Listing struct {
	Photos      []Photo  `json:"photos"`
	Folders     []Folder `json:"folders"`
	Total       int      `json:"total"`
	FolderTotal int      `json:"folder_total"`
	Page        int      `json:"page"`
	Pages       int      `json:"pages"`
	Limit       int      `json:"limit"`
}

func openStore(state string) (*Store, error) {
	db, err := sql.Open("sqlite", filepath.Join(state, "library.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA cache_size=-8192; PRAGMA temp_store=FILE;
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS folders(path TEXT PRIMARY KEY,parent TEXT NOT NULL,name TEXT NOT NULL,sortkey TEXT NOT NULL,device TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS folder_parent ON folders(parent,sortkey,path);
CREATE TABLE IF NOT EXISTS images(id INTEGER PRIMARY KEY,path TEXT UNIQUE NOT NULL,folder TEXT NOT NULL,name TEXT NOT NULL,sortkey TEXT NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,taken INTEGER NOT NULL,width INTEGER NOT NULL,height INTEGER NOT NULL,orientation INTEGER NOT NULL,tags TEXT NOT NULL,search TEXT NOT NULL,error TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS image_folder_name ON images(folder,sortkey,path);
CREATE INDEX IF NOT EXISTS image_folder_date ON images(folder,taken,sortkey,path);
CREATE INDEX IF NOT EXISTS image_name ON images(sortkey,path);
CREATE INDEX IF NOT EXISTS image_date ON images(taken DESC,sortkey,path);
CREATE TABLE IF NOT EXISTS tags(image_id INTEGER REFERENCES images(id) ON DELETE CASCADE,norm TEXT NOT NULL,label TEXT NOT NULL,PRIMARY KEY(image_id,norm));
CREATE INDEX IF NOT EXISTS tag_lookup ON tags(norm,image_id);
CREATE TABLE IF NOT EXISTS ancestors(folder TEXT NOT NULL,image_id INTEGER REFERENCES images(id) ON DELETE CASCADE,PRIMARY KEY(folder,image_id));
CREATE VIRTUAL TABLE IF NOT EXISTS image_search USING fts5(search,content=images,content_rowid=id,tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS image_ai AFTER INSERT ON images BEGIN INSERT INTO image_search(rowid,search) VALUES(new.id,new.search); END;
CREATE TRIGGER IF NOT EXISTS image_ad AFTER DELETE ON images BEGIN INSERT INTO image_search(image_search,rowid,search) VALUES('delete',old.id,old.search); END;
CREATE TRIGGER IF NOT EXISTS image_au AFTER UPDATE OF search ON images BEGIN INSERT INTO image_search(image_search,rowid,search) VALUES('delete',old.id,old.search); INSERT INTO image_search(rowid,search) VALUES(new.id,new.search); END;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db}
	if err = s.initTagCatalog(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

const columns = `i.id,i.path,i.folder,i.name,i.size,i.mtime,i.taken,i.width,i.height,i.orientation,i.tags,i.error`

type scanner interface{ Scan(...any) error }

func readPhoto(row scanner) (p Photo, err error) {
	var tags string
	err = row.Scan(&p.ID, &p.Path, &p.Folder, &p.Name, &p.Size, &p.Mtime, &p.Taken, &p.Width, &p.Height, &p.Orientation, &tags, &p.Error)
	if err == nil {
		err = json.Unmarshal([]byte(tags), &p.Tags)
	}
	return
}
func (s *Store) photo(ctx context.Context, id int64) (Photo, error) {
	return readPhoto(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM images i WHERE i.id=?`, id))
}

func (s *Store) put(ctx context.Context, p Photo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	encoded, _ := json.Marshal(p.Tags)
	_, err = tx.ExecContext(ctx, `INSERT INTO images(path,folder,name,sortkey,size,mtime,taken,width,height,orientation,tags,search,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET folder=excluded.folder,name=excluded.name,sortkey=excluded.sortkey,size=excluded.size,mtime=excluded.mtime,taken=excluded.taken,width=excluded.width,height=excluded.height,orientation=excluded.orientation,tags=excluded.tags,search=excluded.search,error=excluded.error`, p.Path, p.Folder, p.Name, natural(p.Name), p.Size, p.Mtime, p.Taken, p.Width, p.Height, p.Orientation, string(encoded), fold(p.Name+"\n"+strings.Join(p.Tags, "\n")), p.Error)
	if err != nil {
		return err
	}
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM images WHERE path=?`, p.Path).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE image_id=?`, id); err != nil {
		return err
	}
	for _, tag := range p.Tags {
		if _, err = tx.ExecContext(ctx, `INSERT INTO tags VALUES(?,?,?)`, id, fold(tag), tag); err != nil {
			return err
		}
	}
	for folder := p.Folder; ; folder = parent(folder) {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO ancestors VALUES(?,?)`, folder, id); err != nil {
			return err
		}
		if folder == "" {
			break
		}
	}
	return tx.Commit()
}
func parent(p string) string {
	p = path.Dir(p)
	if p == "." {
		return ""
	}
	return p
}

func queryWhere(q Query) (string, []any) {
	w := []string{"1=1"}
	args := []any{}
	if q.Search == "" && len(q.ExactTags) == 0 || q.Scope == "folder" {
		w = append(w, "i.folder=?")
		args = append(args, q.Folder)
	} else if q.Scope != "all" && q.Folder != "" {
		w = append(w, "i.id IN (SELECT image_id FROM ancestors WHERE folder=?)")
		args = append(args, q.Folder)
	}
	var fts []string
	for _, term := range strings.Fields(fold(q.Search)) {
		// Combine trigrams in FTS itself to intersect before materializing ids.
		if len([]rune(term)) >= 3 && strings.IndexFunc(term, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) < 0 {
			fts = append(fts, `"`+term+`"`)
		}
		w = append(w, "instr(i.search,?)>0")
		args = append(args, term)
	}
	if len(fts) > 0 {
		w = append(w, `i.id IN (SELECT rowid FROM image_search WHERE image_search MATCH ?)`)
		args = append(args, strings.Join(fts, " AND "))
	}

	for _, tag := range q.ExactTags {
		w = append(w, "EXISTS (SELECT 1 FROM tags t WHERE t.image_id=i.id AND t.norm=?)")
		args = append(args, fold(tag))
	}
	return strings.Join(w, " AND "), args
}

func (s *Store) list(ctx context.Context, q Query) (Listing, error) {
	out := Listing{Photos: []Photo{}, Folders: []Folder{}, Page: q.Page, Limit: q.Limit}
	text, tags, err := parseSearch(q.Search)
	if err != nil {
		return out, err
	}
	q.Search = text
	q.ExactTags = cleanTags(append(append(q.ExactTags, tags...), q.Tag))
	where, args := queryWhere(q)
	countSQL := `SELECT count(*) FROM images i WHERE ` + where
	countArgs := args
	if len(q.ExactTags) == 1 && q.Search == "" && (q.Scope == "all" || q.Scope == "tree" && q.Folder == "") {
		// The tag lookup index already contains the full result set. Avoid
		// probing it once for every image when counting a library-wide tag.
		countSQL = `SELECT COALESCE((SELECT count FROM tag_catalog WHERE norm=?),0)`
		countArgs = []any{fold(q.ExactTags[0])}
	}
	if err := s.db.QueryRowContext(ctx, countSQL, countArgs...).Scan(&out.Total); err != nil {
		return out, err
	}
	if q.Search == "" && len(q.ExactTags) == 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM folders WHERE parent=? AND path<>''`, q.Folder).Scan(&out.FolderTotal); err != nil {
			return out, err
		}
	}
	out.Pages = max(1, (max(out.Total, out.FolderTotal)+q.Limit-1)/q.Limit)
	out.Page = min(q.Page, out.Pages)
	order := "i.sortkey,i.path"
	if q.Sort == "date" {
		order = "i.taken DESC,i.sortkey,i.path"
	}
	params := append(append([]any{}, args...), q.Limit, (out.Page-1)*q.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM images i WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, params...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		p, e := readPhoto(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Photos = append(out.Photos, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if q.Search == "" && len(q.ExactTags) == 0 {
		folderOrder := "f.sortkey,f.path"
		if q.Sort == "date" {
			folderOrder = "COALESCE((SELECT max(i.taken) FROM ancestors a JOIN images i ON i.id=a.image_id WHERE a.folder=f.path),0) DESC,f.sortkey,f.path"
		}
		rows, err = s.db.QueryContext(ctx, `SELECT f.path,f.name,(SELECT count(*) FROM ancestors a WHERE a.folder=f.path),COALESCE((SELECT a.image_id FROM ancestors a WHERE a.folder=f.path ORDER BY a.image_id LIMIT 1),0) FROM folders f WHERE f.parent=? AND f.path<>'' ORDER BY `+folderOrder+` LIMIT ? OFFSET ?`, q.Folder, q.Limit, (out.Page-1)*q.Limit)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var f Folder
			if err = rows.Scan(&f.Path, &f.Name, &f.Count, &f.Cover); err != nil {
				break
			}
			out.Folders = append(out.Folders, f)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	return out, err
}
