package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Limits on chat fields, in characters.
const (
	MaxChatTitle = 80
	MaxChatGoal  = 2000
)

// Reasons a chat was destroyed.
const (
	DestroyByCreator = "creator"
	DestroyIdle      = "idle"
	DestroyExpired   = "expired"
)

// Chat is a temporary conversation. A destroyed chat keeps only this
// metadata.
type Chat struct {
	ID            string
	Title         string
	Goal          string
	CreatorID     int64
	CreatedAt     int64
	ExpiresAt     int64
	LastSequence  int64
	LastMessageAt int64
	DestroyedAt   int64
	DestroyReason string
}

// Destroyed reports whether the chat is gone.
func (c Chat) Destroyed() bool { return c.DestroyedAt != 0 }

// ChatInfo is a chat with its presence figures, as of the time and lease
// it was read with. The figures mean nothing for a destroyed chat.
type ChatInfo struct {
	Chat
	// OnlineCount and ActiveCount count participants whose lease is
	// running and participants that have not left.
	OnlineCount int
	ActiveCount int
	// IdleSince is when the chat last had someone online, or will stop
	// having someone if leases are not renewed; it is the creation time if
	// nobody ever entered.
	IdleSince int64
}

const chatColumns = `c.id, c.title, c.goal, c.creator_id, c.created_at, c.expires_at,
	c.last_sequence, c.last_message_at, c.destroyed_at, c.destroy_reason`

// presenceColumns adds ChatInfo's figures; it takes the current time and
// the lease (both milliseconds) as the parameters ?1 and ?2.
const presenceColumns = `,
	(SELECT count(*) FROM participations p WHERE p.chat_id = c.id AND p.left_at IS NULL AND p.last_seen_at + ?2 > ?1),
	(SELECT count(*) FROM participations p WHERE p.chat_id = c.id AND p.left_at IS NULL),
	max(c.created_at, coalesce((SELECT max(CASE WHEN p.left_at IS NULL THEN p.last_seen_at + ?2 ELSE p.left_at END)
		FROM participations p WHERE p.chat_id = c.id), 0))`

type scanner interface{ Scan(...any) error }

func scanChat(row scanner, extra ...any) (Chat, error) {
	var c Chat
	var lastMessage, destroyed sql.NullInt64
	var reason sql.NullString
	err := row.Scan(append([]any{&c.ID, &c.Title, &c.Goal, &c.CreatorID, &c.CreatedAt, &c.ExpiresAt,
		&c.LastSequence, &lastMessage, &destroyed, &reason}, extra...)...)
	c.LastMessageAt, c.DestroyedAt, c.DestroyReason = lastMessage.Int64, destroyed.Int64, reason.String
	return c, err
}

func scanChatInfo(row scanner) (ChatInfo, error) {
	var ci ChatInfo
	c, err := scanChat(row, &ci.OnlineCount, &ci.ActiveCount, &ci.IdleSince)
	ci.Chat = c
	return ci, err
}

// CreateChat creates a chat that expires lifetime after now.
func (s *Store) CreateChat(ctx context.Context, creatorID int64, title, goal string, lifetime time.Duration) (Chat, error) {
	title, err := cleanLine("标题", title, MaxChatTitle)
	if err != nil {
		return Chat{}, err
	}
	if utf8.RuneCountInString(goal) > MaxChatGoal {
		return Chat{}, invalid(fmt.Sprintf("沟通目标最多 %d 个字符", MaxChatGoal))
	}
	now := s.nowMilli()
	c := Chat{
		ID: newID("c_", 16), Title: title, Goal: goal, CreatorID: creatorID,
		CreatedAt: now, ExpiresAt: now + lifetime.Milliseconds(),
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO chats (id, title, goal, creator_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Title, c.Goal, c.CreatorID, c.CreatedAt, c.ExpiresAt)
	return c, err
}

// Chat returns a chat, destroyed or not.
func (s *Store) Chat(ctx context.Context, id string) (Chat, error) {
	return chatIn(ctx, s.db, id)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func chatIn(ctx context.Context, q querier, id string) (Chat, error) {
	c, err := scanChat(q.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats c WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// liveChatIn returns a chat that is not destroyed.
func liveChatIn(ctx context.Context, q querier, id string) (Chat, error) {
	c, err := chatIn(ctx, q, id)
	if err == nil && c.Destroyed() {
		err = ErrChatDestroyed
	}
	return c, err
}

// ChatInfo returns a chat with its presence figures.
func (s *Store) ChatInfo(ctx context.Context, id string, lease time.Duration) (ChatInfo, error) {
	ci, err := scanChatInfo(s.db.QueryRowContext(ctx,
		`SELECT `+chatColumns+presenceColumns+` FROM chats c WHERE c.id = ?3`,
		s.nowMilli(), lease.Milliseconds(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return ci, ErrNotFound
	}
	return ci, err
}

// ListUserChats returns the chats the user created or took part in: live
// ones first by latest activity, then destroyed ones by destruction time.
// It reads one more row than limit to report whether more follow.
func (s *Store) ListUserChats(ctx context.Context, userID int64, lease time.Duration, offset, limit int) ([]ChatInfo, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+chatColumns+presenceColumns+`
		FROM chats c
		WHERE c.creator_id = ?3 OR EXISTS (SELECT 1 FROM participations p WHERE p.chat_id = c.id AND p.user_id = ?3)
		ORDER BY c.destroyed_at IS NOT NULL,
			CASE WHEN c.destroyed_at IS NULL THEN coalesce(c.last_message_at, c.created_at) ELSE c.destroyed_at END DESC,
			c.id
		LIMIT ?4 OFFSET ?5`,
		s.nowMilli(), lease.Milliseconds(), userID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []ChatInfo{}
	for rows.Next() {
		ci, err := scanChatInfo(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, ci)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// DestroyChat deletes the chat's messages and ends every participation;
// their credentials stop working at once. The chat row stays as history.
func (s *Store) DestroyChat(ctx context.Context, id, reason string) (Chat, error) {
	var c Chat
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		c, err = destroyIn(ctx, tx, id, reason, s.nowMilli())
		return err
	})
	return c, err
}

func destroyIn(ctx context.Context, tx *sql.Tx, id, reason string, now int64) (Chat, error) {
	c, err := liveChatIn(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = ?`, id); err != nil {
		return c, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET destroyed_at = ?, destroy_reason = ? WHERE id = ?`, now, reason, id); err != nil {
		return c, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE participations SET left_at = ?, end_reason = 'chat_destroyed'
		WHERE chat_id = ? AND left_at IS NULL`, now, id); err != nil {
		return c, err
	}
	c.DestroyedAt, c.DestroyReason = now, reason
	return c, nil
}

// DestroyDue destroys every chat past its lifetime or idle for at least
// idle, and returns them. The check and the destruction share one
// transaction, so a participant arriving meanwhile cannot be cut off.
func (s *Store) DestroyDue(ctx context.Context, lease, idle time.Duration) ([]Chat, error) {
	var out []Chat
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.nowMilli()
		rows, err := tx.QueryContext(ctx, `
			SELECT `+chatColumns+presenceColumns+` FROM chats c WHERE c.destroyed_at IS NULL`,
			now, lease.Milliseconds())
		if err != nil {
			return err
		}
		due := map[string]string{}
		for rows.Next() {
			ci, err := scanChatInfo(rows)
			if err != nil {
				rows.Close()
				return err
			}
			idleAt := ci.IdleSince + idle.Milliseconds()
			switch {
			case now >= ci.ExpiresAt && ci.ExpiresAt <= idleAt:
				due[ci.ID] = DestroyExpired
			case now >= idleAt:
				due[ci.ID] = DestroyIdle
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id, reason := range due {
			c, err := destroyIn(ctx, tx, id, reason, now)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// LapsedChats returns the live chats in which a participant's lease ran
// out during (from, to], so watchers can be told it went offline.
func (s *Store) LapsedChats(ctx context.Context, from, to time.Time, lease time.Duration) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.chat_id FROM participations p JOIN chats c ON c.id = p.chat_id
		WHERE c.destroyed_at IS NULL AND p.left_at IS NULL
			AND p.last_seen_at + ?1 > ?2 AND p.last_seen_at + ?1 <= ?3`,
		lease.Milliseconds(), from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
