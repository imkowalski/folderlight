package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ScanStatus struct {
	Running  bool      `json:"running"`
	Force    bool      `json:"force"`
	Visited  int       `json:"visited"`
	Updated  int       `json:"updated"`
	Errors   int       `json:"errors"`
	Current  string    `json:"current"`
	Message  string    `json:"message"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
}
type Indexer struct {
	store  *Store
	config Config
	mu     sync.Mutex
	status ScanStatus
	wg     sync.WaitGroup
}

func (s *Indexer) snapshot() ScanStatus        { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *Indexer) change(fn func(*ScanStatus)) { s.mu.Lock(); defer s.mu.Unlock(); fn(&s.status) }
func (s *Indexer) start(ctx context.Context, force bool) bool {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return false
	}
	s.status = ScanStatus{Running: true, Force: force, Started: time.Now(), Message: "Scanning library"}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() { defer s.wg.Done(); s.run(ctx, force) }()
	return true
}
func (s *Indexer) warn(p string, err error) {
	log.Printf("scan %q: %v", p, err)
	s.change(func(st *ScanStatus) { st.Errors++; st.Message = fmt.Sprintf("%s: %v", p, err) })
}
func device(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprint(st.Dev)
	}
	return "unknown"
}
func identity(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return "unknown"
}

func (s *Indexer) run(ctx context.Context, force bool) {
	defer s.change(func(st *ScanStatus) {
		st.Running = false
		st.Current = ""
		st.Finished = time.Now()
		if st.Errors == 0 {
			st.Message = "Library is up to date"
		}
	})
	root, err := os.OpenRoot(s.config.Library)
	if err != nil {
		s.warn("library", err)
		return
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		s.warn("library", err)
		return
	}
	var known string
	err = s.store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='library_identity'`).Scan(&known)
	id := s.config.Library + ":" + identity(info)
	if err == nil && known != id {
		s.warn("library", errors.New("library root changed or mount unavailable; preserving index (see README)"))
		return
	}
	if err != nil && err != sql.ErrNoRows {
		s.warn("database", err)
		return
	}
	if _, err = s.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings VALUES('library_identity',?)`, id); err != nil {
		s.warn("database", err)
		return
	}
	if _, err = s.store.db.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS scan_seen(path TEXT PRIMARY KEY); DELETE FROM scan_seen`); err != nil {
		s.warn("database", err)
		return
	}
	if err = s.walk(ctx, root, "", force); err != nil {
		s.warn("library", err)
	}
}

func (s *Indexer) walk(ctx context.Context, root *os.Root, folder string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := folder
	if name == "" {
		name = "."
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("not a regular directory")
	}
	var oldDevice string
	err = s.store.db.QueryRowContext(ctx, `SELECT device FROM folders WHERE path=?`, folder).Scan(&oldDevice)
	if err == nil && oldDevice != device(info) {
		return errors.New("directory filesystem changed; preserving index")
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO folders(path,parent,name,sortkey,device) VALUES(?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET name=excluded.name,sortkey=excluded.sortkey`, folder, parent(folder), path.Base(folder), natural(path.Base(folder)), device(info))
	if err != nil {
		return err
	}
	complete := false
	for {
		entries, readErr := f.ReadDir(256)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			relative := path.Join(folder, entry.Name())
			s.change(func(st *ScanStatus) { st.Current = relative })
			// Record every entry before stat: failed reads must never be treated as deletions.
			if _, err = s.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO scan_seen VALUES(?)`, relative); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				if err = s.walk(ctx, root, relative, force); err != nil {
					s.warn(relative, err)
				}
				continue
			}
			if !supported(entry.Name()) {
				continue
			}
			info, err := root.Lstat(relative)
			if err != nil {
				s.warn(relative, err)
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			s.change(func(st *ScanStatus) { st.Visited++ })
			var size, mtime int64
			err = s.store.db.QueryRowContext(ctx, `SELECT size,mtime FROM images WHERE path=?`, relative).Scan(&size, &mtime)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if !force && err == nil && size == info.Size() && mtime == info.ModTime().UnixNano() {
				continue
			}
			imageFile, err := openSource(root, relative)
			if err != nil {
				s.warn(relative, err)
				continue
			}
			p := Photo{Path: relative, Folder: folder, Name: entry.Name(), Size: info.Size(), Mtime: info.ModTime().UnixNano(), Taken: info.ModTime().Unix(), Orientation: 1, Tags: []string{}}
			if mediaKind(p.Name) == "image" {
				readMetadata(imageFile, &p)
			} else {
				readExtendedMetadata(ctx, imageFile, &p, s.config.State)
			}
			imageFile.Close()
			if err = s.store.put(ctx, p); err != nil {
				return err
			}
			s.change(func(st *ScanStatus) { st.Updated++ })
			if p.Error != "" {
				s.warn(relative, errors.New(p.Error))
			}
		}
		if readErr == io.EOF {
			complete = true
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if !complete {
		return nil
	}
	// Reconcile only a directory whose complete enumeration succeeded.
	if _, err = s.store.db.ExecContext(ctx, `DELETE FROM images WHERE folder=? AND path NOT IN (SELECT path FROM scan_seen)`, folder); err != nil {
		return err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT path FROM folders WHERE parent=? AND path<>'' AND path NOT IN (SELECT path FROM scan_seen)`, folder)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			break
		}
		removed = append(removed, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range removed {
		if err = s.removeTree(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
func (s *Indexer) removeTree(ctx context.Context, p string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM images WHERE id IN (SELECT image_id FROM ancestors WHERE folder=?)`, p); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM folders WHERE path=? OR substr(path,1,length(?)+1)=?`, p, p, p+"/"); err != nil {
		return err
	}
	return tx.Commit()
}

func validPath(p string) bool { return p == "" || fs.ValidPath(p) && !strings.Contains(p, "\\") }
func openSource(root *os.Root, p string) (*os.File, error) {
	if !validPath(p) || p == "" {
		return nil, errors.New("invalid library path")
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlinks are not served")
		}
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("not a regular image")
	}
	return f, nil
}
