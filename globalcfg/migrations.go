package g

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"main/globalcfg/migrationdefs"
)

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;`

const aiMetadataSchema = `
CREATE TABLE IF NOT EXISTS ai_chat_models (
    chat_id INTEGER PRIMARY KEY,
    model TEXT NOT NULL,
    show_usage INTEGER NOT NULL DEFAULT 0 CHECK (show_usage IN (0, 1))
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS ai_session_meta (
    session_id INTEGER PRIMARY KEY,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    gemini_interaction_id TEXT,
    window_start_msg_id INTEGER,
    gemini_cache_name TEXT,
    gemini_cache_expire_time INTEGER,
    gemini_cache_start_msg_id INTEGER,
    gemini_cache_end_msg_id INTEGER,
    gemini_cache_token_count INTEGER NOT NULL DEFAULT 0,
    gemini_cache_fingerprint TEXT,
    history_rebuild_lossy INTEGER NOT NULL DEFAULT 0 CHECK (history_rebuild_lossy IN (0, 1)),
    FOREIGN KEY (session_id) REFERENCES gemini_sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ai_message_meta (
    session_id INTEGER NOT NULL,
    msg_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    input_message_count INTEGER NOT NULL DEFAULT 0,
    input_first_msg_id INTEGER NOT NULL DEFAULT 0,
    input_last_msg_id INTEGER NOT NULL DEFAULT 0,
    assistant_payload BLOB,
    assistant_payload_format TEXT,
    PRIMARY KEY (session_id, msg_id),
    FOREIGN KEY (session_id, msg_id) REFERENCES gemini_contents(session_id, msg_id) ON DELETE CASCADE
) WITHOUT ROWID;
`

type databaseMigration struct {
	version int64
	name    string
	source  string
	offline bool
	run     func(context.Context, *sql.Tx) error
}

var mainDatabaseMigrations = []databaseMigration{
	{
		version: 1,
		name:    "ai_metadata_baseline",
		source:  migrationdefs.AIMetadataBaselineSource,
		run:     migrateAIMetadataBaseline,
	},
	{
		version: 2,
		name:    "remove_legacy_ai_memory",
		source:  migrationdefs.RemoveLegacyAIMemorySource,
		run:     migrateRemoveLegacyAIMemory,
	},
	{
		version: 3,
		name:    "generic_ai_v2",
		source:  migrationdefs.AIV2OfflineSource,
		offline: true,
		run:     func(context.Context, *sql.Tx) error { return nil },
	},
}

func migrationChecksum(source string) string {
	return migrationdefs.Checksum(source)
}

func runDatabaseMigrations(database *sql.DB) error {
	return applyDatabaseMigrations(context.Background(), database, mainDatabaseMigrations)
}

func applyDatabaseMigrations(ctx context.Context, database *sql.DB, migrations []databaseMigration) error {
	if _, err := database.ExecContext(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var previous int64
	for _, migration := range migrations {
		if migration.version <= previous {
			return fmt.Errorf("database migrations are not strictly ordered at version %d", migration.version)
		}
		previous = migration.version
		checksum := migrationChecksum(migration.source)
		var savedName, savedChecksum string
		err := database.QueryRowContext(ctx,
			`SELECT name, checksum FROM schema_migrations WHERE version = ?`, migration.version).
			Scan(&savedName, &savedChecksum)
		switch {
		case err == nil:
			if savedName != migration.name || savedChecksum != checksum {
				return fmt.Errorf("database migration %d checksum mismatch: database=%s/%s code=%s/%s",
					migration.version, savedName, savedChecksum, migration.name, checksum)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read database migration %d: %w", migration.version, err)
		}
		if migration.offline {
			return fmt.Errorf("database migration %d (%s) requires the offline migration tool",
				migration.version, migration.name)
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin database migration %d: %w", migration.version, err)
		}
		if err = migration.run(ctx, tx); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, unixepoch())`, migration.version, migration.name, checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply database migration %d (%s): %w", migration.version, migration.name, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit database migration %d (%s): %w", migration.version, migration.name, err)
		}
	}
	return nil
}

func migrateAIMetadataBaseline(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, aiMetadataSchema); err != nil {
		return err
	}
	columns := []struct {
		table, name, definition string
	}{
		{"ai_chat_models", "show_usage", "INTEGER NOT NULL DEFAULT 0 CHECK (show_usage IN (0, 1))"},
		{"ai_message_meta", "chat_id", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "output_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "cached_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "input_message_count", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "input_first_msg_id", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "input_last_msg_id", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_message_meta", "assistant_payload", "BLOB"},
		{"ai_message_meta", "assistant_payload_format", "TEXT"},
		{"ai_session_meta", "gemini_interaction_id", "TEXT"},
		{"ai_session_meta", "window_start_msg_id", "INTEGER"},
		{"ai_session_meta", "gemini_cache_name", "TEXT"},
		{"ai_session_meta", "gemini_cache_expire_time", "INTEGER"},
		{"ai_session_meta", "gemini_cache_start_msg_id", "INTEGER"},
		{"ai_session_meta", "gemini_cache_end_msg_id", "INTEGER"},
		{"ai_session_meta", "gemini_cache_token_count", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_session_meta", "gemini_cache_fingerprint", "TEXT"},
		{"ai_session_meta", "history_rebuild_lossy", "INTEGER NOT NULL DEFAULT 0 CHECK (history_rebuild_lossy IN (0, 1))"},
	}
	for _, column := range columns {
		if err := addMigrationColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_message_meta
SET chat_id = COALESCE((SELECT chat_id FROM gemini_contents
    WHERE gemini_contents.session_id = ai_message_meta.session_id
      AND gemini_contents.msg_id = ai_message_meta.msg_id), chat_id)
WHERE chat_id = 0`); err != nil {
		return err
	}
	if err := migrateStableAIPromptVariables(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_message_meta_chat_msg
ON ai_message_meta(chat_id, msg_id)`)
	return err
}

func addMigrationColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found = found || name == column
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func migrateStableAIPromptVariables(ctx context.Context, tx *sql.Tx) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name='gemini_system_prompt')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	replacements := [][2]string{
		{"%TIME%", "见最新用户消息头中的时间（Asia/Shanghai）"},
		{"%DATE%", "见最新用户消息头中的日期（Asia/Shanghai）"},
		{"%DATETIME%", "见最新用户消息头中的日期和时间（Asia/Shanghai）"},
		{"%DATETIME_TZ%", "见最新用户消息头中的日期和时间（Asia/Shanghai）"},
		{"%WEEKDAY%", "可根据最新用户消息头中的日期推算"},
		{"%MSG_DATETIME%", "见最新用户消息头中的日期和时间（Asia/Shanghai）"},
		{"%SENDER_NAME%", "见最新用户消息头中的显示名"},
		{"%MSG_ID%", "不可用"},
		{"%SENDER_USERNAME%", "不可用"},
		{"%SENDER_ID%", "不可用"},
		{"%QUOTE%", "不可用"},
	}
	for _, replacement := range replacements {
		if _, err := tx.ExecContext(ctx, `UPDATE gemini_system_prompt
SET prompt=replace(prompt, ?, ?) WHERE instr(prompt, ?) > 0`,
			replacement[0], replacement[1], replacement[0]); err != nil {
			return err
		}
	}
	return nil
}

func migrateRemoveLegacyAIMemory(ctx context.Context, tx *sql.Tx) error {
	var promptsExist bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name='gemini_system_prompt')`).Scan(&promptsExist); err != nil {
		return err
	}
	if promptsExist {
		if _, err := tx.ExecContext(ctx, `UPDATE gemini_system_prompt
SET prompt=replace(prompt, '%MEMORIES%', '') WHERE instr(prompt, '%MEMORIES%') > 0`); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS gemini_memories`)
	return err
}
