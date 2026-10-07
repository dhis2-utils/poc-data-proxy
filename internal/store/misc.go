package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// --- HTTP cache (spec 5.1) ---------------------------------------------------

type CachedResponse struct {
	Status    int
	Header    http.Header
	Body      []byte
	FetchedAt string
}

func cacheAAD(userID int64, method, url string) string {
	return fmt.Sprintf("cache:%d:%s:%s", userID, method, url)
}

func (s *Store) PutCache(userID int64, method, url string, r *CachedResponse) error {
	hb, _ := json.Marshal(r.Header)
	aad := cacheAAD(userID, method, url)
	hct, err := s.Seal(aad+":h", hb)
	if err != nil {
		return err
	}
	bct, err := s.Seal(aad+":b", r.Body)
	if err != nil {
		return err
	}
	_, err = s.DB().Exec(`INSERT INTO http_cache(user_id, method, url, status, headers_enc, body_enc, fetched_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(user_id, method, url) DO UPDATE SET status=excluded.status,
 headers_enc=excluded.headers_enc, body_enc=excluded.body_enc, fetched_at=excluded.fetched_at`,
		userID, method, url, r.Status, hct, bct, Now())
	return err
}

// GetCache returns the cached response or nil.
func (s *Store) GetCache(userID int64, method, url string) (*CachedResponse, error) {
	var hct, bct []byte
	r := &CachedResponse{}
	err := s.DB().QueryRow(`SELECT status, headers_enc, body_enc, fetched_at FROM http_cache WHERE user_id=? AND method=? AND url=?`,
		userID, method, url).Scan(&r.Status, &hct, &bct, &r.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	aad := cacheAAD(userID, method, url)
	hb, err := s.OpenBlob(aad+":h", hct)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(hb, &r.Header)
	r.Body, err = s.OpenBlob(aad+":b", bct)
	return r, err
}

// GetCacheAnyUser returns a cached response for url from any user. Only used for
// static app files, which hold no user data.
func (s *Store) GetCacheAnyUser(method, url string) (*CachedResponse, error) {
	var id int64
	err := s.DB().QueryRow(`SELECT user_id FROM http_cache WHERE method=? AND url=? ORDER BY fetched_at DESC LIMIT 1`,
		method, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetCache(id, method, url)
}

// --- issues (spec 11.2) ---------------------------------------------------------

type Issue struct {
	ID                int64
	OutboxSeq         int64
	ObjectUID         string
	Kind              string
	UserID            int64
	Error             json.RawMessage
	CreatedAt         string
	ReportedCentralAt string
}

func (s *Store) AddIssue(seq int64, uid, kind string, userID int64, errJSON []byte) error {
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO issues(outbox_seq, object_uid, kind, user_id, error_enc, created_at) VALUES(?,?,?,?,?,?)`,
		seq, uid, kind, userID, []byte{}, Now())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	ct, err := s.Seal(fmt.Sprintf("issue:%d", id), errJSON)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE issues SET error_enc=? WHERE id=?`, ct, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Issues(onlyUnreported bool, limit int) ([]*Issue, error) {
	q := `SELECT id, outbox_seq, object_uid, kind, user_id, error_enc, created_at, reported_central_at FROM issues`
	if onlyUnreported {
		q += ` WHERE reported_central_at=''`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.DB().Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Issue
	for rows.Next() {
		is := &Issue{}
		var ct []byte
		if err := rows.Scan(&is.ID, &is.OutboxSeq, &is.ObjectUID, &is.Kind, &is.UserID, &ct, &is.CreatedAt,
			&is.ReportedCentralAt); err != nil {
			return nil, err
		}
		b, err := s.OpenBlob(fmt.Sprintf("issue:%d", is.ID), ct)
		if err != nil {
			return nil, err
		}
		is.Error = b
		out = append(out, is)
	}
	return out, rows.Err()
}

func (s *Store) MarkIssueReported(id int64) error {
	_, err := s.DB().Exec(`UPDATE issues SET reported_central_at=? WHERE id=?`, Now(), id)
	return err
}

func (s *Store) CountIssues() int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM issues`).Scan(&n)
	return n
}

// --- pull state (spec 11.3) -----------------------------------------------------

func (s *Store) PullState(userID int64, program string) (lastPull, lastFull string) {
	s.DB().QueryRow(`SELECT last_pull_at, last_full_at FROM pull_state WHERE user_id=? AND program=?`, userID, program).
		Scan(&lastPull, &lastFull)
	return
}

func (s *Store) SetPullState(userID int64, program, lastPull string, full bool) error {
	lastFull := ""
	if full {
		lastFull = lastPull
	}
	_, err := s.DB().Exec(`INSERT INTO pull_state(user_id, program, last_pull_at, last_full_at) VALUES(?,?,?,?)
ON CONFLICT(user_id, program) DO UPDATE SET last_pull_at=excluded.last_pull_at,
 last_full_at=CASE WHEN excluded.last_full_at<>'' THEN excluded.last_full_at ELSE pull_state.last_full_at END`,
		userID, program, lastPull, lastFull)
	return err
}

func (s *Store) LastPull() string {
	var t sql.NullString
	s.DB().QueryRow(`SELECT max(last_pull_at) FROM pull_state`).Scan(&t)
	return t.String
}

// --- reserved values (spec 10.3) ------------------------------------------------

type ReservedValue struct {
	Attribute string
	Value     json.RawMessage // the object central returned for this value
	ExpiresAt string
}

func (s *Store) AddReservedValues(attr string, values []ReservedValue) error {
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range values {
		ct, err := s.Seal("rv:"+attr, v.Value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO reserved_values(attribute, value_enc, expires_at) VALUES(?,?,?)`,
			attr, ct, v.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TakeReservedValues removes and returns up to n unexpired values. A value is
// deleted in the same transaction it is read in, so it is never handed out twice.
func (s *Store) TakeReservedValues(attr string, n int, now string) ([]ReservedValue, error) {
	tx, err := s.DB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM reserved_values WHERE attribute=? AND expires_at<>'' AND expires_at<?`, attr, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id, value_enc, expires_at FROM reserved_values WHERE attribute=? ORDER BY id LIMIT ?`, attr, n)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var out []ReservedValue
	for rows.Next() {
		var id int64
		var ct []byte
		var exp string
		if err := rows.Scan(&id, &ct, &exp); err != nil {
			rows.Close()
			return nil, err
		}
		b, err := s.OpenBlob("rv:"+attr, ct)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		out = append(out, ReservedValue{Attribute: attr, Value: b, ExpiresAt: exp})
	}
	rows.Close()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM reserved_values WHERE id=?`, id); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func (s *Store) CountReservedValues(attr, now string) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM reserved_values WHERE attribute=? AND (expires_at='' OR expires_at>=?)`, attr, now).Scan(&n)
	return n
}
