package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// User is a key slot (key file) with, while unlocked, the user's row in the data file.
// Secret fields are decrypted on demand.
type User struct {
	ID         int64
	Username   string
	KEKSalt    []byte
	WrappedDK  []byte
	NeedsLogin bool

	// From the data file; empty while locked.
	UserUID           string
	PATUID            string
	PATExpires        string
	AccessRefreshedAt string
	LastLoginAt       string
	HasPAT            bool
	HasPassword       bool

	patEnc, pwEnc, accessEnc []byte
}

const slotCols = `id, username, kek_salt, wrapped_dk, needs_login`

func scanSlot(sc interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var needs int
	if err := sc.Scan(&u.ID, &u.Username, &u.KEKSalt, &u.WrappedDK, &needs); err != nil {
		return nil, err
	}
	u.NeedsLogin = needs != 0
	return u, nil
}

// fill adds the user's data file row, if the store is unlocked and it exists.
func (s *Store) fill(u *User) (*User, error) {
	err := s.DB().QueryRow(`SELECT user_uid, pat_enc, pat_uid, pat_expires, pw_enc, access_json_enc,
 access_refreshed_at, last_login_at FROM users WHERE id=?`, u.ID).Scan(&u.UserUID, &u.patEnc, &u.PATUID,
		&u.PATExpires, &u.pwEnc, &u.accessEnc, &u.AccessRefreshedAt, &u.LastLoginAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	u.HasPAT = len(u.patEnc) > 0
	u.HasPassword = len(u.pwEnc) > 0
	return u, nil
}

// UserByName returns the user or nil if unknown.
func (s *Store) UserByName(username string) (*User, error) {
	u, err := scanSlot(s.Keys.QueryRow(`SELECT `+slotCols+` FROM key_slots WHERE username=?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.fill(u)
}

func (s *Store) UserByID(id int64) (*User, error) {
	u, err := scanSlot(s.Keys.QueryRow(`SELECT `+slotCols+` FROM key_slots WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.fill(u)
}

func (s *Store) Users() ([]*User, error) {
	rows, err := s.Keys.Query(`SELECT ` + slotCols + ` FROM key_slots ORDER BY username`)
	if err != nil {
		return nil, err
	}
	var out []*User
	for rows.Next() {
		u, err := scanSlot(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, u := range out {
		if _, err := s.fill(u); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpsertCredentials creates the user or replaces their key slot. The store must be
// unlocked: the slot wraps the in-memory data key.
func (s *Store) UpsertCredentials(username, userUID string, salt, wrapped []byte) (int64, error) {
	if !s.Unlocked() {
		return 0, ErrLocked
	}
	_, err := s.Keys.Exec(`INSERT INTO key_slots(username, kek_salt, wrapped_dk) VALUES(?,?,?)
ON CONFLICT(username) DO UPDATE SET kek_salt=excluded.kek_salt, wrapped_dk=excluded.wrapped_dk, needs_login=0`,
		username, salt, wrapped)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.Keys.QueryRow(`SELECT id FROM key_slots WHERE username=?`, username).Scan(&id); err != nil {
		return 0, err
	}
	_, err = s.write(`INSERT INTO users(id, username, user_uid) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET username=excluded.username, user_uid=excluded.user_uid`, id, username, userUID)
	return id, err
}

// TouchLogin records a login. It leaves needs_login alone: only an online login, which
// central accepted, clears it.
func (s *Store) TouchLogin(id int64) error {
	_, err := s.write(`UPDATE users SET last_login_at=? WHERE id=?`, Now(), id)
	return err
}

// PruneKeySlots removes the key slots of users who have not logged in within maxAge,
// except the keep most recently used ones, so a reboot can always be unlocked. Every
// slot lets someone holding the disk test password guesses offline; a slot nobody
// uses is only risk. The user gets a new slot at their next online login.
func (s *Store) PruneKeySlots(maxAge time.Duration, keep int) ([]string, error) {
	users, err := s.Users()
	if err != nil {
		return nil, err
	}
	var active []*User
	for _, u := range users {
		if len(u.WrappedDK) > 0 {
			active = append(active, u)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].LastLoginAt > active[j].LastLoginAt })
	cutoff := time.Now().Add(-maxAge)
	var pruned []string
	for i, u := range active {
		if i < keep || ParseTime(u.LastLoginAt).After(cutoff) {
			continue
		}
		if _, err := s.Keys.Exec(`UPDATE key_slots SET wrapped_dk=NULL, kek_salt=x'' WHERE id=?`, u.ID); err != nil {
			return pruned, err
		}
		pruned = append(pruned, u.Username)
	}
	return pruned, nil
}

// SetNeedsLogin is in the key file, so an offline login can refuse before unlock.
func (s *Store) SetNeedsLogin(id int64, v bool) error {
	n := 0
	if v {
		n = 1
	}
	_, err := s.Keys.Exec(`UPDATE key_slots SET needs_login=? WHERE id=?`, n, id)
	return err
}

func (s *Store) SetPAT(id int64, key, uid, expires string) error {
	var ct []byte
	if key != "" {
		var err error
		if ct, err = s.Seal(fmt.Sprintf("pat:%d", id), []byte(key)); err != nil {
			return err
		}
	}
	_, err := s.write(`UPDATE users SET pat_enc=?, pat_uid=?, pat_expires=? WHERE id=?`, ct, uid, expires, id)
	return err
}

func (s *Store) PAT(u *User) (string, error) {
	if len(u.patEnc) == 0 {
		return "", nil
	}
	b, err := s.OpenBlob(fmt.Sprintf("pat:%d", u.ID), u.patEnc)
	return string(b), err
}

// SetPassword stores the encrypted password. Only used when the user cannot create a
// PAT on central (spec 7.1 fallback).
func (s *Store) SetPassword(id int64, pw string) error {
	var ct []byte
	if pw != "" {
		var err error
		if ct, err = s.Seal(fmt.Sprintf("pw:%d", id), []byte(pw)); err != nil {
			return err
		}
	}
	_, err := s.write(`UPDATE users SET pw_enc=? WHERE id=?`, ct, id)
	return err
}

func (s *Store) Password(u *User) (string, error) {
	if len(u.pwEnc) == 0 {
		return "", nil
	}
	b, err := s.OpenBlob(fmt.Sprintf("pw:%d", u.ID), u.pwEnc)
	return string(b), err
}

func (s *Store) SetAccess(id int64, accessJSON []byte) error {
	ct, err := s.Seal(fmt.Sprintf("access:%d", id), accessJSON)
	if err != nil {
		return err
	}
	_, err = s.write(`UPDATE users SET access_json_enc=?, access_refreshed_at=? WHERE id=?`, ct, Now(), id)
	return err
}

func (s *Store) Access(u *User) ([]byte, error) {
	if len(u.accessEnc) == 0 {
		return nil, nil
	}
	return s.OpenBlob(fmt.Sprintf("access:%d", u.ID), u.accessEnc)
}
