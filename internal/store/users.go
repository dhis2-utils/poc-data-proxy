package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// User is a row of the users table. Secret fields are decrypted on demand.
type User struct {
	ID                int64
	Username          string
	UserUID           string
	PwHash            string
	KEKSalt           []byte
	WrappedDK         []byte
	PATUID            string
	PATExpires        string
	AccessRefreshedAt string
	LastLoginAt       string
	NeedsLogin        bool
	HasPAT            bool
	HasPassword       bool

	patEnc, pwEnc, accessEnc []byte
}

const userCols = `id, username, user_uid, pw_hash, kek_salt, wrapped_dk, pat_enc, pat_uid, pat_expires, pw_enc,
 access_json_enc, access_refreshed_at, last_login_at, needs_login`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var needs int
	err := sc.Scan(&u.ID, &u.Username, &u.UserUID, &u.PwHash, &u.KEKSalt, &u.WrappedDK, &u.patEnc, &u.PATUID,
		&u.PATExpires, &u.pwEnc, &u.accessEnc, &u.AccessRefreshedAt, &u.LastLoginAt, &needs)
	if err != nil {
		return nil, err
	}
	u.NeedsLogin = needs != 0
	u.HasPAT = len(u.patEnc) > 0
	u.HasPassword = len(u.pwEnc) > 0
	return u, nil
}

// UserByName returns the user or nil if unknown.
func (s *Store) UserByName(username string) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) UserByID(id int64) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) Users() ([]*User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertCredentials creates the user or replaces their password hash and key slot.
func (s *Store) UpsertCredentials(username, userUID, pwHash string, salt, wrapped []byte) (int64, error) {
	_, err := s.DB.Exec(`INSERT INTO users(username, user_uid, pw_hash, kek_salt, wrapped_dk) VALUES(?,?,?,?,?)
ON CONFLICT(username) DO UPDATE SET user_uid=excluded.user_uid, pw_hash=excluded.pw_hash,
 kek_salt=excluded.kek_salt, wrapped_dk=excluded.wrapped_dk, needs_login=0`, username, userUID, pwHash, salt, wrapped)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRow(`SELECT id FROM users WHERE username=?`, username).Scan(&id)
	return id, err
}

func (s *Store) TouchLogin(id int64) error {
	_, err := s.DB.Exec(`UPDATE users SET last_login_at=?, needs_login=0 WHERE id=?`, Now(), id)
	return err
}

func (s *Store) SetNeedsLogin(id int64, v bool) error {
	n := 0
	if v {
		n = 1
	}
	_, err := s.DB.Exec(`UPDATE users SET needs_login=? WHERE id=?`, n, id)
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
	_, err := s.DB.Exec(`UPDATE users SET pat_enc=?, pat_uid=?, pat_expires=? WHERE id=?`, ct, uid, expires, id)
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
	_, err := s.DB.Exec(`UPDATE users SET pw_enc=? WHERE id=?`, ct, id)
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
	_, err = s.DB.Exec(`UPDATE users SET access_json_enc=?, access_refreshed_at=? WHERE id=?`, ct, Now(), id)
	return err
}

func (s *Store) Access(u *User) ([]byte, error) {
	if len(u.accessEnc) == 0 {
		return nil, nil
	}
	return s.OpenBlob(fmt.Sprintf("access:%d", u.ID), u.accessEnc)
}
