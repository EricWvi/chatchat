package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSummaryConflict reports that a newer summary was written since the
// one a new summary was built on.
var ErrSummaryConflict = errors.New("summary conflict")

// Summary is an immutable snapshot of a chat's progress, as of the message
// numbered CoversThrough.
type Summary struct {
	ChatID        string
	Revision      int64
	CoversThrough int64
	Content       string
	Model         string
	CreatedAt     int64
}

const summaryColumns = `chat_id, revision, covers_through_sequence, content, model, created_at`

func scanSummary(row scanner) (Summary, error) {
	var s Summary
	err := row.Scan(&s.ChatID, &s.Revision, &s.CoversThrough, &s.Content, &s.Model, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

func latestSummaryIn(ctx context.Context, q querier, chatID string) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, `
		SELECT `+summaryColumns+` FROM summaries WHERE chat_id = ? ORDER BY revision DESC LIMIT 1`, chatID))
}

// LatestSummary returns the chat's newest summary, or ErrNotFound when it
// has none.
func (s *Store) LatestSummary(ctx context.Context, chatID string) (Summary, error) {
	return latestSummaryIn(ctx, s.db, chatID)
}

// AppendSummary stores the summary that follows revision prev (0 for the
// first) and covers the messages through covers. It fails with
// ErrSummaryConflict when prev is no longer the newest revision, and with
// ErrChatDestroyed once the chat is gone. covers must lie beyond the
// previous summary and within the chat's messages.
func (s *Store) AppendSummary(ctx context.Context, chatID string, prev, covers int64, content, model string) (Summary, error) {
	sum := Summary{ChatID: chatID, Revision: prev + 1, CoversThrough: covers, Content: content, Model: model}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := liveChatIn(ctx, tx, chatID)
		if err != nil {
			return err
		}
		latest, err := latestSummaryIn(ctx, tx, chatID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		switch {
		case latest.Revision != prev:
			return ErrSummaryConflict
		case covers <= latest.CoversThrough || covers > c.LastSequence:
			return invalid(fmt.Sprintf("summary covers sequence %d, outside (%d, %d]", covers, latest.CoversThrough, c.LastSequence))
		}
		sum.CreatedAt = s.nowMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO summaries (`+summaryColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
			sum.ChatID, sum.Revision, sum.CoversThrough, sum.Content, sum.Model, sum.CreatedAt)
		return err
	})
	return sum, err
}

// CountText counts the chat's text messages after the sequence after.
func (s *Store) CountText(ctx context.Context, chatID string, after int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM messages WHERE chat_id = ? AND sequence > ? AND kind = 'text'`,
		chatID, after).Scan(&n)
	return n, err
}

// LiveChatIDs returns the IDs of the chats that are not destroyed.
func (s *Store) LiveChatIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chats WHERE destroyed_at IS NULL`)
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
