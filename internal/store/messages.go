package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Kinds of message.
const (
	KindText   = "text"
	KindSystem = "system"
)

// Limits on message input, in characters.
const (
	MaxMessageChars = 16000
	MaxRequestID    = 64
	MaxReason       = 500
)

// System message texts; the subject is the message's participant.
const (
	joinedText      = "加入了 chat"
	leftText        = "退出了 chat"
	proposedEndText = "建议结束"
)

// Message is a numbered entry in a chat. Sequences start at 1 and have no
// gaps.
type Message struct {
	ID              string
	ChatID          string
	Sequence        int64
	Kind            string
	ParticipationID string
	DisplayName     string
	Text            string
	CreatedAt       int64
}

// SendText adds a text message from a participant. With a request ID, a
// retry of the same text returns the first message and created is false;
// different text under the same ID is ErrRequestIDConflict.
func (s *Store) SendText(ctx context.Context, p Participation, text, requestID string) (m Message, created bool, err error) {
	if strings.TrimSpace(text) == "" {
		return m, false, invalid("消息不能为空")
	}
	if utf8.RuneCountInString(text) > MaxMessageChars {
		return m, false, fmt.Errorf("%w: 消息最多 %d 个字符", ErrTooLarge, MaxMessageChars)
	}
	if requestID != "" && (utf8.RuneCountInString(requestID) > MaxRequestID || strings.TrimSpace(requestID) != requestID) {
		return m, false, invalid(fmt.Sprintf("client_request_id 最多 %d 个字符，且不能有首尾空白", MaxRequestID))
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if requestID != "" {
			prev, err := scanMessage(tx.QueryRowContext(ctx, `
				SELECT `+messageColumns+` FROM messages WHERE participation_id = ? AND client_request_id = ?`,
				p.ID, requestID))
			switch {
			case err == nil && prev.Text == text:
				m = prev
				return nil
			case err == nil:
				return ErrRequestIDConflict
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		var err error
		m, err = appendMessage(ctx, tx, p, KindText, text, requestID, s.nowMilli())
		created = err == nil
		return err
	})
	return m, created, err
}

// ProposeEnd records that a participant suggests ending the chat.
func (s *Store) ProposeEnd(ctx context.Context, p Participation, reason string) (Message, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxReason {
		return Message{}, invalid(fmt.Sprintf("理由最多 %d 个字符", MaxReason))
	}
	text := proposedEndText
	if reason != "" {
		text += "：" + reason
	}
	var m Message
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		m, err = appendMessage(ctx, tx, p, KindSystem, text, "", s.nowMilli())
		return err
	})
	return m, err
}

// appendMessage numbers and stores a message. It fails with
// ErrChatDestroyed or ErrParticipationLeft when the participant can no
// longer write.
func appendMessage(ctx context.Context, tx *sql.Tx, p Participation, kind, text, requestID string, now int64) (Message, error) {
	var left sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT left_at FROM participations WHERE id = ?`, p.ID).Scan(&left); err != nil {
		return Message{}, err
	}
	m := Message{
		ID: newID("m_", 12), ChatID: p.ChatID, Kind: kind, ParticipationID: p.ID,
		DisplayName: p.DisplayName, Text: text, CreatedAt: now,
	}
	// Destruction also ends participations, so the chat is checked first to
	// report the more useful reason. An error rolls the numbering back.
	err := tx.QueryRowContext(ctx, `
		UPDATE chats SET last_sequence = last_sequence + 1, last_message_at = ?
		WHERE id = ? AND destroyed_at IS NULL RETURNING last_sequence`, now, p.ChatID).Scan(&m.Sequence)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return m, ErrChatDestroyed
	case err != nil:
		return m, err
	case left.Valid:
		return m, ErrParticipationLeft
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages (id, chat_id, sequence, kind, participation_id, display_name, text, client_request_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatID, m.Sequence, m.Kind, m.ParticipationID, m.DisplayName, m.Text, nullString(requestID), m.CreatedAt)
	return m, err
}

const messageColumns = `id, chat_id, sequence, kind, participation_id, display_name, text, created_at`

func scanMessage(row scanner) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.ChatID, &m.Sequence, &m.Kind, &m.ParticipationID, &m.DisplayName, &m.Text, &m.CreatedAt)
	return m, err
}

// Messages returns up to limit messages in ascending order: those after
// the sequence after, or, when before is positive, the last ones before
// it. hasMore reports whether further messages lie beyond the page in the
// direction read.
func (s *Store) Messages(ctx context.Context, chatID string, after, before int64, limit int) (out []Message, hasMore bool, err error) {
	query := `SELECT ` + messageColumns + ` FROM messages WHERE chat_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`
	bound := after
	if before > 0 {
		query = `SELECT ` + messageColumns + ` FROM messages WHERE chat_id = ? AND sequence < ? ORDER BY sequence DESC LIMIT ?`
		bound = before
	}
	rows, err := s.db.QueryContext(ctx, query, chatID, bound, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out = []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		out, hasMore = out[:limit], true
	}
	if before > 0 {
		slices.Reverse(out)
	}
	return out, hasMore, nil
}
