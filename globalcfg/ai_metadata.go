package g

import (
	"context"
	"database/sql"
	"fmt"
)

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
    PRIMARY KEY (session_id, msg_id),
    FOREIGN KEY (session_id, msg_id) REFERENCES gemini_contents(session_id, msg_id) ON DELETE CASCADE
) WITHOUT ROWID;
`

func initAIMetadataSchema(database *sql.DB) error {
	if _, err := database.Exec(aiMetadataSchema); err != nil {
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
	}
	for _, column := range columns {
		if err := ensureAIColumn(database, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := database.Exec(`UPDATE ai_message_meta
SET chat_id = COALESCE((SELECT chat_id FROM gemini_contents
    WHERE gemini_contents.session_id = ai_message_meta.session_id
      AND gemini_contents.msg_id = ai_message_meta.msg_id), chat_id)
WHERE chat_id = 0`); err != nil {
		return err
	}
	_, err := database.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_message_meta_chat_msg
ON ai_message_meta(chat_id, msg_id)`)
	return err
}

func ensureAIColumn(database *sql.DB, table, column, definition string) error {
	rows, err := database.Query(`PRAGMA table_info(` + table + `)`)
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
	_, err = database.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func GetAIChatModel(ctx context.Context, chatID int64, fallback string) (string, error) {
	var model string
	err := db.QueryRowContext(ctx, `SELECT model FROM ai_chat_models WHERE chat_id = ?`, chatID).Scan(&model)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	return model, err
}

func SetAIChatModel(ctx context.Context, chatID int64, model string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO ai_chat_models(chat_id, model) VALUES (?, ?)
ON CONFLICT(chat_id) DO UPDATE SET model=excluded.model`, chatID, model)
	return err
}

func GetAIChatUsageEnabled(ctx context.Context, chatID int64) (bool, error) {
	return getAIChatUsageEnabled(ctx, db, chatID)
}

func getAIChatUsageEnabled(ctx context.Context, database *sql.DB, chatID int64) (bool, error) {
	var enabled bool
	err := database.QueryRowContext(ctx, `SELECT show_usage FROM ai_chat_models WHERE chat_id = ?`, chatID).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return enabled, err
}

func ToggleAIChatUsage(ctx context.Context, chatID int64, fallbackModel string) (bool, error) {
	return toggleAIChatUsage(ctx, db, chatID, fallbackModel)
}

func toggleAIChatUsage(ctx context.Context, database *sql.DB, chatID int64, fallbackModel string) (bool, error) {
	var enabled bool
	err := database.QueryRowContext(ctx, `INSERT INTO ai_chat_models(chat_id, model, show_usage) VALUES (?, ?, 1)
ON CONFLICT(chat_id) DO UPDATE SET show_usage = NOT ai_chat_models.show_usage
RETURNING show_usage`, chatID, fallbackModel).Scan(&enabled)
	return enabled, err
}

func GetAISessionModel(ctx context.Context, sessionID int64) (provider, model string, err error) {
	err = db.QueryRowContext(ctx, `SELECT provider, model FROM ai_session_meta WHERE session_id = ?`, sessionID).
		Scan(&provider, &model)
	return
}

func SetAISessionModel(ctx context.Context, sessionID int64, provider, model string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO ai_session_meta(session_id, provider, model) VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET provider=excluded.provider, model=excluded.model`, sessionID, provider, model)
	return err
}

func IncrementAICachedTokens(ctx context.Context, sessionID, tokens int64) error {
	_, err := db.ExecContext(ctx, `UPDATE ai_session_meta
SET cached_input_tokens=cached_input_tokens + ? WHERE session_id = ?`, tokens, sessionID)
	return err
}

type AIMessageUsage struct {
	Provider          string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
}

func SetAIMessageUsage(ctx context.Context, sessionID, msgID, chatID int64, provider, model string,
	inputTokens, outputTokens, cachedInputTokens int64,
) error {
	return setAIMessageUsage(ctx, db, sessionID, msgID, chatID, provider, model,
		inputTokens, outputTokens, cachedInputTokens)
}

func setAIMessageUsage(ctx context.Context, database *sql.DB, sessionID, msgID, chatID int64,
	provider, model string, inputTokens, outputTokens, cachedInputTokens int64,
) error {
	_, err := database.ExecContext(ctx, `INSERT INTO ai_message_meta(
session_id, msg_id, chat_id, provider, model, input_tokens, output_tokens, cached_input_tokens)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, msg_id) DO UPDATE SET
chat_id=excluded.chat_id, provider=excluded.provider, model=excluded.model,
input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
cached_input_tokens=excluded.cached_input_tokens`,
		sessionID, msgID, chatID, provider, model, inputTokens, outputTokens, cachedInputTokens)
	return err
}

func GetAIMessageUsage(ctx context.Context, chatID, msgID int64) (usage AIMessageUsage, err error) {
	return getAIMessageUsage(ctx, db, chatID, msgID)
}

func getAIMessageUsage(ctx context.Context, database *sql.DB, chatID, msgID int64) (usage AIMessageUsage, err error) {
	err = database.QueryRowContext(ctx, `SELECT provider, model, input_tokens, output_tokens, cached_input_tokens
FROM ai_message_meta WHERE chat_id = ? AND msg_id = ?`, chatID, msgID).Scan(
		&usage.Provider, &usage.Model, &usage.InputTokens, &usage.OutputTokens, &usage.CachedInputTokens)
	return
}
