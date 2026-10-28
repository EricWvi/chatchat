package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is someone who signed in through the identity provider.
type User struct {
	ID    int64
	Name  string
	Email string
}

// DisplayName is how the user appears as a web participant.
func (u User) DisplayName() string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return "用户"
}

// Identity is what the identity provider vouches for. Issuer and Subject
// identify the person; Name and Email are display attributes that may
// change between logins.
type Identity struct {
	Issuer  string
	Subject string
	Name    string
	Email   string
}

// UpsertUser returns the user bound to the identity, creating it on first
// login and refreshing the display attributes on later ones.
func (s *Store) UpsertUser(ctx context.Context, id Identity) (User, error) {
	if id.Issuer == "" || id.Subject == "" {
		return User{}, invalid("identity is missing the issuer or subject")
	}
	u := User{Name: id.Name, Email: id.Email}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM users WHERE issuer = ? AND subject = ?`, id.Issuer, id.Subject).Scan(&u.ID)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE users SET name = ?, email = ? WHERE id = ?`, id.Name, id.Email, u.ID)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (issuer, subject, name, email) VALUES (?, ?, ?, ?)`,
			id.Issuer, id.Subject, id.Name, id.Email)
		if err != nil {
			return err
		}
		u.ID, err = res.LastInsertId()
		return err
	})
	return u, err
}

// CreateSession starts a session for the user and returns the secret the
// browser presents. Only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64) (string, error) {
	token := newSecret("")
	now := s.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now, now)
	return token, err
}

// SessionUser resolves a session secret to its user and reports when the
// session was last refreshed.
func (s *Store) SessionUser(ctx context.Context, token string) (User, time.Time, error) {
	var u User
	var lastSeen int64
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, u.email, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hashToken(token)).Scan(&u.ID, &u.Name, &u.Email, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return u, time.Time{}, ErrNotFound
	}
	return u, time.Unix(lastSeen, 0), err
}

// TouchSession records that the session is still in use.
func (s *Store) TouchSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, s.Now().Unix(), hashToken(token))
	return err
}

// DeleteSession ends a session. Deleting an unknown session is not an error.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}
