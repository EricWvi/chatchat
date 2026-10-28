package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// UserTokenPrefix starts every user token. It also lets the server tell a
// user token sent to a participant endpoint from a wrong secret.
const UserTokenPrefix = "cct_"

// MaxTokenName caps the name a user gives a token, in characters.
const MaxTokenName = 64

// ErrTokenRevoked reports a user token that was revoked.
var ErrTokenRevoked = errors.New("token revoked")

// UserToken is a user token's metadata; the token itself is never stored.
type UserToken struct {
	ID         string
	UserID     int64
	Name       string
	CreatedAt  int64
	LastUsedAt int64
}

// CreateUserToken creates a token for the user and returns it with the
// token string, which exists only in this return value.
func (s *Store) CreateUserToken(ctx context.Context, userID int64, name string) (UserToken, string, error) {
	name, err := cleanLine("name", name, MaxTokenName)
	if err != nil {
		return UserToken{}, "", err
	}
	token := newSecret(UserTokenPrefix)
	t := UserToken{ID: newID("t_", 10), UserID: userID, Name: name, CreatedAt: s.nowMilli()}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO user_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, userID, name, hashToken(token), t.CreatedAt)
	return t, token, err
}

// TokenUser resolves a user token to its user and records the use.
// It returns ErrNotFound for an unknown token and ErrTokenRevoked for a
// revoked one.
func (s *Store) TokenUser(ctx context.Context, token string) (User, error) {
	var u User
	if !strings.HasPrefix(token, UserTokenPrefix) {
		return u, ErrNotFound
	}
	var id string
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.revoked_at, u.id, u.name, u.email
		FROM user_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, hashToken(token)).Scan(&id, &revoked, &u.ID, &u.Name, &u.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if revoked.Valid {
		return u, ErrTokenRevoked
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_tokens SET last_used_at = ? WHERE id = ?`, s.nowMilli(), id)
	return u, err
}

// ListUserTokens returns the user's tokens that are not revoked, newest
// first.
func (s *Store) ListUserTokens(ctx context.Context, userID int64) ([]UserToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, created_at, last_used_at FROM user_tokens
		WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserToken{}
	for rows.Next() {
		t := UserToken{UserID: userID}
		var lastUsed sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		t.LastUsedAt = lastUsed.Int64
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeUserToken revokes one of the user's tokens; it stops working at
// once. Revoking an unknown or already revoked token returns ErrNotFound.
func (s *Store) RevokeUserToken(ctx context.Context, userID int64, id string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		s.nowMilli(), id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
