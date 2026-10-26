package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Kinds of participation.
const (
	KindWeb = "web"
	KindCLI = "cli"
)

// Participant statuses.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusLeft    = "left"
)

// Why a participation ended.
const (
	EndLeft          = "left"
	EndChatDestroyed = "chat_destroyed"
)

// ParticipationSecretPrefix starts every participation secret.
const ParticipationSecretPrefix = "ccp_"

// MaxDisplayName caps a participant's display name, in characters.
const MaxDisplayName = 48

// Participation is one identity of a user in a chat.
type Participation struct {
	ID          string
	ChatID      string
	UserID      int64
	Kind        string
	DisplayName string
	JoinedAt    int64
	LastSeenAt  int64
	LeftAt      int64
	EndReason   string

	secretHash string
}

// Status reports the participant's status at now (milliseconds).
func (p Participation) Status(now int64, lease time.Duration) string {
	switch {
	case p.LeftAt != 0:
		return StatusLeft
	case p.LastSeenAt+lease.Milliseconds() > now:
		return StatusOnline
	}
	return StatusOffline
}

// SecretMatches reports in constant time whether secret is this CLI
// participation's secret. It holds even after the participation ended.
func (p Participation) SecretMatches(secret string) bool {
	return hashMatches(secret, p.secretHash)
}

const participationColumns = `id, chat_id, user_id, kind, display_name, secret_hash,
	joined_at, last_seen_at, left_at, end_reason`

func scanParticipation(row scanner) (Participation, error) {
	var p Participation
	var hash, reason sql.NullString
	var left sql.NullInt64
	err := row.Scan(&p.ID, &p.ChatID, &p.UserID, &p.Kind, &p.DisplayName, &hash,
		&p.JoinedAt, &p.LastSeenAt, &left, &reason)
	p.secretHash, p.LeftAt, p.EndReason = hash.String, left.Int64, reason.String
	return p, err
}

func participationIn(ctx context.Context, q querier, id string) (Participation, error) {
	p, err := scanParticipation(q.QueryRowContext(ctx,
		`SELECT `+participationColumns+` FROM participations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Participation returns a participation, ended or not.
func (s *Store) Participation(ctx context.Context, id string) (Participation, error) {
	return participationIn(ctx, s.db, id)
}

// WebParticipation returns the user's web participation in the chat.
func (s *Store) WebParticipation(ctx context.Context, chatID string, userID int64) (Participation, error) {
	p, err := scanParticipation(s.db.QueryRowContext(ctx, `
		SELECT `+participationColumns+` FROM participations
		WHERE chat_id = ? AND user_id = ? AND kind = 'web'`, chatID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Join creates a new CLI participation and returns it with its secret,
// which exists only in this return value. Every call creates a new
// identity; nothing is matched or reused.
func (s *Store) Join(ctx context.Context, chatID string, userID int64, displayName string) (Participation, string, error) {
	name, err := cleanLine("显示名", displayName, MaxDisplayName)
	if err != nil {
		return Participation{}, "", err
	}
	secret := newSecret(ParticipationSecretPrefix)
	now := s.nowMilli()
	p := Participation{
		ID: newID("p_", 16), ChatID: chatID, UserID: userID, Kind: KindCLI, DisplayName: name,
		JoinedAt: now, LastSeenAt: now, secretHash: hashToken(secret),
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := liveChatIn(ctx, tx, chatID); err != nil {
			return err
		}
		if err := insertParticipation(ctx, tx, p); err != nil {
			return err
		}
		_, err := appendMessage(ctx, tx, p, KindSystem, joinedText, "", now)
		return err
	})
	if err != nil {
		return Participation{}, "", err
	}
	return p, secret, nil
}

// Enter finds or creates the user's web participation in the chat and
// marks it online. entered is true when the user newly joined or came back
// after leaving; both are announced with a system message.
func (s *Store) Enter(ctx context.Context, chatID string, u User) (p Participation, entered bool, err error) {
	now := s.nowMilli()
	name := truncate(u.DisplayName(), MaxDisplayName)
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := liveChatIn(ctx, tx, chatID); err != nil {
			return err
		}
		p, err = scanParticipation(tx.QueryRowContext(ctx, `
			SELECT `+participationColumns+` FROM participations
			WHERE chat_id = ? AND user_id = ? AND kind = 'web'`, chatID, u.ID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			p = Participation{
				ID: newID("p_", 16), ChatID: chatID, UserID: u.ID, Kind: KindWeb, DisplayName: name,
				JoinedAt: now, LastSeenAt: now,
			}
			if err := insertParticipation(ctx, tx, p); err != nil {
				return err
			}
			entered = true
		case err != nil:
			return err
		case p.LeftAt != 0:
			p.DisplayName, p.LastSeenAt, p.LeftAt, p.EndReason = name, now, 0, ""
			if _, err := tx.ExecContext(ctx, `
				UPDATE participations SET display_name = ?, last_seen_at = ?, left_at = NULL, end_reason = NULL
				WHERE id = ?`, name, now, p.ID); err != nil {
				return err
			}
			entered = true
		default:
			p.LastSeenAt = now
			_, err := tx.ExecContext(ctx, `UPDATE participations SET last_seen_at = ? WHERE id = ?`, now, p.ID)
			return err
		}
		_, err = appendMessage(ctx, tx, p, KindSystem, joinedText, "", now)
		return err
	})
	return p, entered, err
}

func insertParticipation(ctx context.Context, tx *sql.Tx, p Participation) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO participations (id, chat_id, user_id, kind, display_name, secret_hash, joined_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ChatID, p.UserID, p.Kind, p.DisplayName, nullString(p.secretHash), p.JoinedAt, p.LastSeenAt)
	return err
}

// Touch renews a participant's lease and reports whether that brought it
// back online.
func (s *Store) Touch(ctx context.Context, id string, lease time.Duration) (bool, error) {
	var cameOnline bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		p, err := participationIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.LeftAt != 0 {
			return ErrParticipationLeft
		}
		now := s.nowMilli()
		cameOnline = p.Status(now, lease) == StatusOffline
		_, err = tx.ExecContext(ctx, `UPDATE participations SET last_seen_at = ? WHERE id = ?`, now, id)
		return err
	})
	return cameOnline, err
}

// Leave ends a participation and announces it. A CLI participation's
// secret stops working at once.
func (s *Store) Leave(ctx context.Context, id string) (Participation, error) {
	var p Participation
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if p, err = participationIn(ctx, tx, id); err != nil {
			return err
		}
		now := s.nowMilli()
		// appendMessage rejects a destroyed chat or a participant that left.
		if _, err := appendMessage(ctx, tx, p, KindSystem, leftText, "", now); err != nil {
			return err
		}
		p.LeftAt, p.EndReason = now, EndLeft
		_, err = tx.ExecContext(ctx,
			`UPDATE participations SET left_at = ?, end_reason = ? WHERE id = ?`, now, EndLeft, id)
		return err
	})
	return p, err
}

// Participants returns every participation in the chat, including those
// that ended, in joining order.
func (s *Store) Participants(ctx context.Context, chatID string) ([]Participation, error) {
	return s.participations(ctx, `chat_id = ? ORDER BY joined_at, id`, chatID)
}

// UserParticipations returns the user's participations, newest first,
// optionally only those in one chat.
func (s *Store) UserParticipations(ctx context.Context, userID int64, chatID string) ([]Participation, error) {
	if chatID != "" {
		return s.participations(ctx, `user_id = ? AND chat_id = ? ORDER BY joined_at DESC, id`, userID, chatID)
	}
	return s.participations(ctx, `user_id = ? ORDER BY joined_at DESC, id`, userID)
}

func (s *Store) participations(ctx context.Context, where string, args ...any) ([]Participation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+participationColumns+` FROM participations WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Participation{}
	for rows.Next() {
		p, err := scanParticipation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
