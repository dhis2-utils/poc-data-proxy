package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// The public file (hub.db.public) holds central's answers that central gives anyone
// without a login: the login app's files, its settings and its images. They are in
// plain text, apart from the data file, because the hub must serve the login page
// while the store is locked: after a restart, the store opens only once a known user
// has logged in, through that page. Nothing in it is about a person or a facility's
// data; it is not part of the store's backups (the USB stick) and fills again online.
//
// Its size is capped (PublicMax): anyone on the network can ask for paths under the
// login app's, and each new release of the login app has new file names. The oldest
// copies go first.
const PublicMax = 16 << 20

// PublicPath is the public file next to the key file at path.
func PublicPath(path string) string { return path + ".public" }

func openPublic(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS public_cache(url TEXT PRIMARY KEY, status INTEGER NOT NULL,
 headers TEXT NOT NULL, body BLOB NOT NULL, fetched_at TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// PutPublic keeps central's answer to a GET of url for anyone, locked or not.
func (s *Store) PutPublic(url string, r *CachedResponse) error {
	if len(r.Body) > PublicMax/2 {
		return nil
	}
	hb, _ := json.Marshal(r.Header)
	tx, err := s.pub.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO public_cache(url, status, headers, body, fetched_at) VALUES(?,?,?,?,?)
 ON CONFLICT(url) DO UPDATE SET status=excluded.status, headers=excluded.headers, body=excluded.body, fetched_at=excluded.fetched_at`,
		url, r.Status, string(hb), r.Body, Now()); err != nil {
		return err
	}
	// The oldest copies go until the rest fits.
	if _, err := tx.Exec(`DELETE FROM public_cache WHERE url IN (SELECT url FROM (SELECT url,
 sum(length(body)) OVER (ORDER BY fetched_at DESC, url) AS total FROM public_cache) WHERE total > ?)`, PublicMax); err != nil {
		return err
	}
	return tx.Commit()
}

// GetPublic returns the public copy of url, or nil.
func (s *Store) GetPublic(url string) (*CachedResponse, error) {
	r := &CachedResponse{}
	var hb string
	err := s.pub.QueryRow(`SELECT status, headers, body, fetched_at FROM public_cache WHERE url=?`, url).
		Scan(&r.Status, &hb, &r.Body, &r.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Header = http.Header{}
	json.Unmarshal([]byte(hb), &r.Header)
	return r, nil
}

// HasPublic reports whether the public file holds a copy of url.
func (s *Store) HasPublic(url string) bool {
	var n int
	s.pub.QueryRow(`SELECT count(*) FROM public_cache WHERE url=?`, url).Scan(&n)
	return n > 0
}
