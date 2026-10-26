package store

import (
	"database/sql"
	"fmt"
)

// A migration runs inside its own transaction.
type migration func(tx *sql.Tx) error

func execSQL(query string) migration {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(query)
		return err
	}
}

// migrations are applied in order; PRAGMA user_version records progress.
// Never edit a released migration, append a new one instead.
var migrations = []migration{
	execSQL(`
	CREATE TABLE users (
		id         INTEGER PRIMARY KEY,
		issuer     TEXT NOT NULL,
		subject    TEXT NOT NULL,
		name       TEXT NOT NULL DEFAULT '',
		email      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		UNIQUE (issuer, subject)
	);

	-- Sessions never expire; a row lives until the user signs out.
	-- Times are Unix seconds.
	CREATE TABLE sessions (
		token_hash   TEXT    PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		created_at   INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL
	);
	CREATE INDEX sessions_user ON sessions (user_id);

	-- From here on times are Unix milliseconds.

	-- User tokens for the CLI. A revoked token keeps its row so a request
	-- with it can be told "revoked" rather than "unknown".
	CREATE TABLE user_tokens (
		id           TEXT    PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		name         TEXT    NOT NULL,
		token_hash   TEXT    NOT NULL UNIQUE,
		created_at   INTEGER NOT NULL,
		last_used_at INTEGER,
		revoked_at   INTEGER
	);
	CREATE INDEX user_tokens_user ON user_tokens (user_id);

	-- A destroyed chat keeps its row as account history; its messages are
	-- deleted. last_sequence hands out message sequences.
	CREATE TABLE chats (
		id              TEXT    PRIMARY KEY,
		title           TEXT    NOT NULL,
		goal            TEXT    NOT NULL DEFAULT '',
		creator_id      INTEGER NOT NULL REFERENCES users (id),
		created_at      INTEGER NOT NULL,
		expires_at      INTEGER NOT NULL,
		last_sequence   INTEGER NOT NULL DEFAULT 0,
		last_message_at INTEGER,
		destroyed_at    INTEGER,
		destroy_reason  TEXT
	);
	CREATE INDEX chats_creator ON chats (creator_id);
	CREATE INDEX chats_live ON chats (expires_at) WHERE destroyed_at IS NULL;

	-- One identity of a user in a chat. A web participation is unique per
	-- (chat, user) and is revived on re-entry; a CLI participation is new
	-- on every join and authenticates with secret_hash. Rows outlive the
	-- chat; the hash then only tells a stale credential from a wrong one.
	CREATE TABLE participations (
		id           TEXT    PRIMARY KEY,
		chat_id      TEXT    NOT NULL REFERENCES chats (id),
		user_id      INTEGER NOT NULL REFERENCES users (id),
		kind         TEXT    NOT NULL CHECK (kind IN ('web', 'cli')),
		display_name TEXT    NOT NULL,
		secret_hash  TEXT,
		joined_at    INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		left_at      INTEGER,
		end_reason   TEXT
	);
	CREATE UNIQUE INDEX participations_web ON participations (chat_id, user_id) WHERE kind = 'web';
	CREATE INDEX participations_chat ON participations (chat_id);
	CREATE INDEX participations_user ON participations (user_id);

	-- display_name is a snapshot taken when the message was sent.
	-- client_request_id makes a retried send return the first message.
	CREATE TABLE messages (
		id                TEXT    PRIMARY KEY,
		chat_id           TEXT    NOT NULL REFERENCES chats (id),
		sequence          INTEGER NOT NULL,
		kind              TEXT    NOT NULL CHECK (kind IN ('text', 'system')),
		participation_id  TEXT    NOT NULL REFERENCES participations (id),
		display_name      TEXT    NOT NULL,
		text              TEXT    NOT NULL,
		client_request_id TEXT,
		created_at        INTEGER NOT NULL,
		UNIQUE (chat_id, sequence)
	);
	CREATE UNIQUE INDEX messages_request ON messages (participation_id, client_request_id)
		WHERE client_request_id IS NOT NULL;
	`),
	execSQL(`
	-- Summary snapshots, append-only. Each covers the messages up to
	-- covers_through_sequence and is deleted with the chat's messages.
	CREATE TABLE summaries (
		chat_id                 TEXT    NOT NULL REFERENCES chats (id),
		revision                INTEGER NOT NULL,
		covers_through_sequence INTEGER NOT NULL,
		content                 TEXT    NOT NULL,
		model                   TEXT    NOT NULL,
		created_at              INTEGER NOT NULL,
		PRIMARY KEY (chat_id, revision)
	);
	`),
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := migrations[i](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}
