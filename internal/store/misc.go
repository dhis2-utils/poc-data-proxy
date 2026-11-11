package store

import (
	"context"
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

// CountCacheBefore counts a user's copies of API answers fetched before t (apps'
// files are checked with central at each use, so they are not counted).
func (s *Store) CountCacheBefore(userID int64, t time.Time) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM http_cache WHERE user_id=? AND fetched_at<? AND url LIKE '/api/%' AND url NOT LIKE '/api/apps/%'`,
		userID, t.UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n
}

// GetCacheAnyUser returns a cached response for url from any user. Only used for
// static app files, which hold no user data. A logged-in user's 200 comes first:
// a newer anonymous answer may be a redirect to the login page (review R-16).
func (s *Store) GetCacheAnyUser(method, url string) (*CachedResponse, error) {
	var id int64
	err := s.DB().QueryRow(`SELECT user_id FROM http_cache WHERE method=? AND url=?
 ORDER BY status=200 DESC, user_id>0 DESC, fetched_at DESC LIMIT 1`,
		method, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetCache(id, method, url)
}

// GetAppShell returns the latest cached 200 for an app page /apps/<app>, of any app
// and user: central's global shell (2.42+) answers every app with the same page, and
// switching apps in the shell never loads the next app's page, so a reload offline
// asks for one the hub may not hold.
func (s *Store) GetAppShell() (*CachedResponse, error) {
	var id int64
	var url string
	err := s.DB().QueryRow(`SELECT user_id, url FROM http_cache WHERE method='GET' AND status=200 AND url LIKE '/apps/_%'
 AND url NOT LIKE '/apps/%/%' AND url NOT LIKE '/apps/%?%' ORDER BY fetched_at DESC LIMIT 1`).Scan(&id, &url)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetCache(id, "GET", url)
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
	// Through Tx: a write outside the store's write lock waits in SQLite's busy
	// handler, which gave up with "database is locked" on a busy Pi (review R-46).
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
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
		_, err = tx.Exec(`UPDATE issues SET error_enc=? WHERE id=?`, ct, id)
		return err
	})
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

// OpenIssuesOf returns the open issues of one author, newest first: the hub's page
// shows staff their own refused changes.
func (s *Store) OpenIssuesOf(userID int64, limit int) ([]*Issue, error) {
	return s.issues(`SELECT id, outbox_seq, object_uid, kind, user_id, error_enc, created_at, reported_central_at FROM issues i
 WHERE i.user_id = ? AND `+sqlIssueOpen+` ORDER BY id DESC LIMIT ?`, userID, OutboxAggregate, OutboxRejected, OutboxPartial,
		SyncRejected, limit)
}

// OpenIssueObjectsOf are the objects of one author's open issues.
func (s *Store) OpenIssueObjectsOf(userID int64) ([]string, error) {
	rows, err := s.DB().Query(`SELECT DISTINCT object_uid FROM issues i WHERE i.user_id = ? AND `+sqlIssueOpen, userID,
		OutboxAggregate, OutboxRejected, OutboxPartial, SyncRejected)
	var uids []string
	err = scanRows(rows, err, func(r *sql.Rows) error {
		var u string
		err := r.Scan(&u)
		uids = append(uids, u)
		return err
	})
	return uids, err
}

// sqlRecordRejected finds a refused object of a record: the person or event itself, or
// one of the person's enrollments and events (both through an index).
const sqlRecordRejected = `SELECT 1 FROM objects WHERE (uid = ? OR te_uid = ?) AND sync_state = ? LIMIT 1`

// RecordHasRejected reports whether a record (a person, or an event program's event)
// holds a refused object. It spares the issues query for the records without one: most
// of them, and a hub can hold thousands of refusals (3 411 on the Pi 4, 9 November).
func (s *Store) RecordHasRejected(record string) bool {
	var one int
	return s.DB().QueryRow(sqlRecordRejected, record, record, SyncRejected).Scan(&one) == nil
}

// sqlIssueOpen is the condition that the issue aliased i is still open; its arguments
// are OutboxAggregate, OutboxRejected, OutboxPartial, SyncRejected. A superseded object's
// refusal is open too: it waits for a hub manager.
const sqlIssueOpen = `((i.kind = ? AND EXISTS (SELECT 1 FROM outbox o WHERE o.seq = i.outbox_seq AND o.state IN (?, ?)))
 OR EXISTS (SELECT 1 FROM objects ob WHERE ob.uid = i.object_uid AND ob.sync_state IN (?, '` + SyncSuperseded + `')))`

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

// ReportIssuesAgain has the object's issues reported to central again: its refusal was
// superseded, and the issue on central says so.
func (s *Store) ReportIssuesAgain(tx *sql.Tx, uid string) error {
	_, err := tx.Exec(`UPDATE issues SET reported_central_at='' WHERE object_uid=?`, uid)
	return err
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
	return s.Tx(context.Background(), func(tx *sql.Tx) error { // in the write lock (review R-46)
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
		return nil
	})
}

// TakeReservedValues removes and returns up to n unexpired values. A value is
// deleted in the same transaction it is read in, so it is never handed out twice.
func (s *Store) TakeReservedValues(attr string, n int, now string) ([]ReservedValue, error) {
	var out []ReservedValue
	err := s.Tx(context.Background(), func(tx *sql.Tx) error { // in the write lock (review R-46)
		out = nil
		if _, err := tx.Exec(`DELETE FROM reserved_values WHERE attribute=? AND expires_at<>'' AND expires_at<?`, attr, now); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT id, value_enc, expires_at FROM reserved_values WHERE attribute=? ORDER BY id LIMIT ?`, attr, n)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			var ct []byte
			var exp string
			if err := rows.Scan(&id, &ct, &exp); err != nil {
				rows.Close()
				return err
			}
			b, err := s.OpenBlob("rv:"+attr, ct)
			if err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			out = append(out, ReservedValue{Attribute: attr, Value: b, ExpiresAt: exp})
		}
		rows.Close()
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM reserved_values WHERE id=?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DropReservedValues removes all of an attribute's values (its pattern changed).
func (s *Store) DropReservedValues(attr string) (int64, error) {
	res, err := s.write(`DELETE FROM reserved_values WHERE attribute=?`, attr)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) CountReservedValues(attr, now string) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM reserved_values WHERE attribute=? AND (expires_at='' OR expires_at>=?)`, attr, now).Scan(&n)
	return n
}
