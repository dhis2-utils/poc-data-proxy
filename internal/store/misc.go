package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// --- HTTP cache (spec 5.1) ---------------------------------------------------

type CachedResponse struct {
	Status    int
	Header    http.Header
	Body      []byte
	FetchedAt string
	Replayed  bool // loaded at the user's login, not for a request of theirs
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
	_, err = s.write(`INSERT INTO http_cache(user_id, method, url, status, headers_enc, body_enc, fetched_at, replayed)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(user_id, method, url) DO UPDATE SET status=excluded.status,
 headers_enc=excluded.headers_enc, body_enc=excluded.body_enc, fetched_at=excluded.fetched_at, replayed=excluded.replayed`,
		userID, method, url, r.Status, hct, bct, Now(), r.Replayed)
	return err
}

// ReplaySources lists the GETs clients of any user made since seen, most recent
// first, that userID has no copy of fetched since fresh. Copies loaded at a login
// (Replayed) are not sources.
func (s *Store) ReplaySources(userID int64, seen, fresh time.Time) ([]string, error) {
	f := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	rows, err := s.DB().Query(`SELECT url FROM http_cache c WHERE method='GET' AND replayed=0 AND user_id>0
 AND fetched_at>=? AND NOT EXISTS (SELECT 1 FROM http_cache m
 WHERE m.user_id=? AND m.method='GET' AND m.url=c.url AND m.fetched_at>=?)
 GROUP BY url ORDER BY max(fetched_at) DESC`, f(seen), userID, f(fresh))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PruneCache deletes cached responses not refreshed for maxAge. Entries in use are
// refreshed whenever a client fetches them online.
func (s *Store) PruneCache(maxAge time.Duration) (int64, error) {
	res, err := s.write(`DELETE FROM http_cache WHERE fetched_at < ?`, time.Now().Add(-maxAge).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// HasCache reports whether a response for url is cached for the user, or, with
// anyUser, for anyone; without opening it.
func (s *Store) HasCache(userID int64, anyUser bool, method, url string) bool {
	var n int
	if anyUser {
		s.DB().QueryRow(`SELECT count(*) FROM http_cache WHERE method=? AND url=?`, method, url).Scan(&n)
	} else {
		s.DB().QueryRow(`SELECT count(*) FROM http_cache WHERE user_id=? AND method=? AND url=?`, userID, method, url).Scan(&n)
	}
	return n > 0
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

// AddIssue records a refused change. at is when central refused it, by central's clock
// (its answer's Date), so that a later change on central can be told apart (RefusedAt);
// "" is now, by the hub's.
func (s *Store) AddIssue(seq int64, uid, kind string, userID int64, errJSON []byte, at string) error {
	if at == "" {
		at = Now()
	}
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Refused again after a resend: the issue it already has stands, once.
	var n int
	tx.QueryRow(`SELECT count(*) FROM issues WHERE outbox_seq=? AND object_uid=?`, seq, uid).Scan(&n)
	if n > 0 {
		return nil
	}
	res, err := tx.Exec(`INSERT INTO issues(outbox_seq, object_uid, kind, user_id, error_enc, created_at) VALUES(?,?,?,?,?,?)`,
		seq, uid, kind, userID, []byte{}, at)
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
	return s.issues(q+` ORDER BY id DESC LIMIT ?`, limit)
}

// UnresolvedIssues returns the issues still open, newest first: a tracker object still
// marked rejected (sending a fixed version clears it), or an aggregate edit whose
// outbox entry is still refused.
func (s *Store) UnresolvedIssues(limit int) ([]*Issue, error) {
	return s.issues(`SELECT id, outbox_seq, object_uid, kind, user_id, error_enc, created_at, reported_central_at FROM issues i
 WHERE `+sqlIssueOpen+` ORDER BY id DESC LIMIT ?`, OutboxAggregate, OutboxRejected, OutboxPartial, SyncRejected, limit)
}

// sqlIssueOpen is the condition that the issue aliased i is still open; its arguments
// are OutboxAggregate, OutboxRejected, OutboxPartial, SyncRejected.
const sqlIssueOpen = `((i.kind = ? AND EXISTS (SELECT 1 FROM outbox o WHERE o.seq = i.outbox_seq AND o.state IN (?, ?)))
 OR EXISTS (SELECT 1 FROM objects ob WHERE ob.uid = i.object_uid AND ob.sync_state = ?))`

// ResolvedIssues lists the issues no longer open, and whether each was reported to
// central: they are removed there and here, so both hold what is stuck now.
func (s *Store) ResolvedIssues() (ids []int64, reported []bool, err error) {
	rows, err := s.DB().Query(`SELECT id, reported_central_at <> '' FROM issues i WHERE NOT `+sqlIssueOpen,
		OutboxAggregate, OutboxRejected, OutboxPartial, SyncRejected)
	err = scanRows(rows, err, func(r *sql.Rows) error {
		var id int64
		var rep bool
		err := r.Scan(&id, &rep)
		ids, reported = append(ids, id), append(reported, rep)
		return err
	})
	return ids, reported, err
}

func (s *Store) DeleteIssue(id int64) error {
	_, err := s.write(`DELETE FROM issues WHERE id = ?`, id)
	return err
}

func (s *Store) issues(q string, args ...any) ([]*Issue, error) {
	rows, err := s.DB().Query(q, args...)
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

// RefusedAt is when central last refused a change of this object ("" if never).
func (s *Store) RefusedAt(uid string) string {
	var at sql.NullString
	s.DB().QueryRow(`SELECT max(created_at) FROM issues WHERE object_uid = ?`, uid).Scan(&at)
	return at.String
}

func (s *Store) MarkIssueReported(id int64) error {
	_, err := s.write(`UPDATE issues SET reported_central_at=? WHERE id=?`, Now(), id)
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
	_, err := s.write(`INSERT INTO pull_state(user_id, program, last_pull_at, last_full_at) VALUES(?,?,?,?)
ON CONFLICT(user_id, program) DO UPDATE SET last_pull_at=excluded.last_pull_at,
 last_full_at=CASE WHEN excluded.last_full_at<>'' THEN excluded.last_full_at ELSE pull_state.last_full_at END`,
		userID, program, lastPull, lastFull)
	return err
}

// SetFullPullTime records that the user's pull of program was a full one, now.
func (s *Store) SetFullPullTime(userID int64, program string) error {
	_, err := s.write(`UPDATE pull_state SET last_full_at=? WHERE user_id=? AND program=?`, Now(), userID, program)
	return err
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

// CountCreatedSince counts the objects of one kind created at or after since (in
// central's time format): the facility's observed registration rate.
func (s *Store) CountCreatedSince(kind, since string) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM objects WHERE kind=? AND created_at>=?`, kind, since).Scan(&n)
	return n
}
