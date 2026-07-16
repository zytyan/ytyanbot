package mainmigrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"main/globalcfg/migrationdefs"
)

const TableSQL = `
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

// Migration is one checksummed main-database migration. Offline migrations are
// rejected by ApplyRuntime and may only be executed by the offline migrator.
type Migration struct {
	Version int64
	Name    string
	Source  string
	Offline bool
	Run     func(context.Context, *sql.Tx) error
}

var migrations = []Migration{
	{
		Version: 1,
		Name:    "ai_metadata_baseline",
		Source:  migrationdefs.AIMetadataBaselineSource,
		Run:     migrateAIMetadataBaseline,
	},
	{
		Version: 2,
		Name:    "remove_legacy_ai_memory",
		Source:  migrationdefs.RemoveLegacyAIMemorySource,
		Run:     migrateRemoveLegacyAIMemory,
	},
	{
		Version: 3,
		Name:    "generic_ai_v2",
		Source:  migrationdefs.AIV2OfflineSource,
		Offline: true,
		Run:     func(context.Context, *sql.Tx) error { return nil },
	},
	{
		Version: 4,
		Name:    "main_schema_cleanup",
		Source:  migrationdefs.MainSchemaCleanupV4Source,
		Offline: true,
		Run:     migrateMainSchemaCleanup,
	},
	{
		Version: 5,
		Name:    "normalize_chat_stats",
		Source:  migrationdefs.NormalizeChatStatsV5Source,
		Offline: true,
		Run:     migrateNormalizeChatStats,
	},
}

func All() []Migration {
	return append([]Migration(nil), migrations...)
}

func ApplyRuntime(ctx context.Context, database *sql.DB) error {
	return Apply(ctx, database, All(), false)
}

func ApplyOffline(ctx context.Context, database *sql.DB) error {
	return Apply(ctx, database, All(), true)
}

// Apply applies migrations in order. When allowOffline is false, an unapplied
// offline migration is a hard gate rather than an implicit large rewrite.
func Apply(ctx context.Context, database *sql.DB, list []Migration, allowOffline bool) error {
	if _, err := database.ExecContext(ctx, TableSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var previous int64
	for _, migration := range list {
		if migration.Version <= previous {
			return fmt.Errorf("database migrations are not strictly ordered at version %d", migration.Version)
		}
		previous = migration.Version
		checksum := migrationdefs.Checksum(migration.Source)
		var savedName, savedChecksum string
		err := database.QueryRowContext(ctx,
			`SELECT name, checksum FROM schema_migrations WHERE version = ?`, migration.Version).
			Scan(&savedName, &savedChecksum)
		switch {
		case err == nil:
			if savedName != migration.Name || savedChecksum != checksum {
				return fmt.Errorf("database migration %d checksum mismatch: database=%s/%s code=%s/%s",
					migration.Version, savedName, savedChecksum, migration.Name, checksum)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read database migration %d: %w", migration.Version, err)
		}
		if migration.Offline && !allowOffline {
			return fmt.Errorf("database migration %d (%s) requires the offline migration tool",
				migration.Version, migration.Name)
		}
		if migration.Run == nil {
			return fmt.Errorf("database migration %d (%s) has no runner", migration.Version, migration.Name)
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin database migration %d: %w", migration.Version, err)
		}
		if err = migration.Run(ctx, tx); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, unixepoch())`, migration.Version, migration.Name, checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply database migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit database migration %d (%s): %w", migration.Version, migration.Name, err)
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
		if err := addColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
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
	if err := migrateStablePromptVariables(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_message_meta_chat_msg
ON ai_message_meta(chat_id, msg_id)`)
	return err
}

func addColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
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

func migrateStablePromptVariables(ctx context.Context, tx *sql.Tx) error {
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

func migrateMainSchemaCleanup(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE users_v4
(
    user_id    INTEGER      NOT NULL PRIMARY KEY,
    updated_at INT_UNIX_SEC NOT NULL,
    first_name TEXT         NOT NULL,
    last_name  TEXT,
    username   TEXT
) WITHOUT ROWID;

INSERT INTO users_v4(user_id, updated_at, first_name, last_name, username)
SELECT user_id, updated_at, first_name, last_name, username FROM users;
DROP TABLE users;
ALTER TABLE users_v4 RENAME TO users;

CREATE TABLE chat_cfg_v4
(
    id               INTEGER PRIMARY KEY NOT NULL,
    auto_cvt_bili    INT_BOOL            NOT NULL CHECK (auto_cvt_bili IN (0, 1)),
    auto_calculate   INT_BOOL            NOT NULL CHECK (auto_calculate IN (0, 1)),
    auto_exchange    INT_BOOL            NOT NULL CHECK (auto_exchange IN (0, 1)),
    auto_check_adult INT_BOOL            NOT NULL CHECK (auto_check_adult IN (0, 1)),
    enable_coc       INT_BOOL            NOT NULL CHECK (enable_coc IN (0, 1)),
    resp_nsfw_msg    INT_BOOL            NOT NULL CHECK (resp_nsfw_msg IN (0, 1)),
    timezone         INTEGER             NOT NULL CHECK (timezone < 86400 AND timezone > -86400)
);

INSERT INTO chat_cfg_v4(id, auto_cvt_bili, auto_calculate, auto_exchange,
                        auto_check_adult, enable_coc, resp_nsfw_msg, timezone)
SELECT id, auto_cvt_bili, auto_calculate, auto_exchange,
       auto_check_adult, enable_coc, resp_nsfw_msg, timezone
FROM chat_cfg;
DROP TABLE chat_cfg;
ALTER TABLE chat_cfg_v4 RENAME TO chat_cfg;

DROP TABLE IF EXISTS chat_attr;
DROP TABLE IF EXISTS chat_topics;
`)
	return err
}
