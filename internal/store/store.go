// Package store persists users, tokens, chats, participations and
// messages in SQLite.
//
// Times are Unix milliseconds; 0 stands for "not set" in place of NULL.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite" // CGO-free SQLite driver
)

var (
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("not found")
	// ErrInvalid reports rejected input; the wrapped message is user-facing.
	ErrInvalid = errors.New("invalid input")
	// ErrTooLarge reports text over its length limit.
	ErrTooLarge = errors.New("too large")
	// ErrRequestIDConflict reports a client_request_id reused with other text.
	ErrRequestIDConflict = errors.New("request id conflict")
	// ErrChatDestroyed reports an operation on a destroyed chat.
	ErrChatDestroyed = errors.New("chat destroyed")
	// ErrParticipationLeft reports an operation by a participant that left.
	ErrParticipationLeft = errors.New("participation left")
)

// Store is safe for concurrent use.
type Store struct {
	db *sql.DB

	mu  sync.RWMutex
	now func() time.Time
}

// Open opens (and migrates) the database inside dataDir.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "chatchat.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection serialises writes and keeps transactions simple;
	// it is also what makes message sequences gap-free.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the clock, for tests.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Now is the store's current time; every timestamp it writes comes from it.
func (s *Store) Now() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.now()
}

func (s *Store) nowMilli() int64 { return s.Now().UnixMilli() }

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID returns prefix followed by n random bytes in lowercase base32.
// Chat IDs double as invitations, so they carry 128 bits.
func newID(prefix string, n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}

// newSecret returns prefix followed by 32 random bytes.
func newSecret(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// hashToken hashes a high-entropy secret. A fast hash is enough because
// the secret cannot be guessed.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// hashMatches compares a secret with a stored hash in constant time.
func hashMatches(secret, hash string) bool {
	return hash != "" && subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(hash)) == 1
}

// cleanLine trims a single-line name and checks its length in characters.
func cleanLine(field, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", invalid(field + "不能为空")
	}
	if utf8.RuneCountInString(s) > max {
		return "", invalid(fmt.Sprintf("%s最多 %d 个字符", field, max))
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", invalid(field + "不能包含换行或控制字符")
	}
	return s, nil
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v != 0} }

func nullString(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
