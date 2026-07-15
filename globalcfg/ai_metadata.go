package g

import (
	"context"
	"database/sql"
	"fmt"
)

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

// ChangeAISessionModel changes an existing historical session. The previous
// remote Gemini chain cannot be reused across models/providers, so the next
// request must bootstrap from a lossy, locally stored history representation.
func ChangeAISessionModel(ctx context.Context, sessionID int64, provider, model string) error {
	return changeAISessionModel(ctx, db, sessionID, provider, model)
}

func changeAISessionModel(ctx context.Context, executor AIResponseExecutor, sessionID int64, provider, model string) error {
	result, err := executor.ExecContext(ctx, `UPDATE ai_session_meta
SET provider = ?, model = ?, gemini_interaction_id = NULL, history_rebuild_lossy = 1,
    gemini_cache_name = NULL, gemini_cache_expire_time = NULL,
    gemini_cache_start_msg_id = NULL, gemini_cache_end_msg_id = NULL,
    gemini_cache_token_count = 0, gemini_cache_fingerprint = NULL
WHERE session_id = ?`, provider, model, sessionID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("change AI session model: session %d not found", sessionID)
	}
	return nil
}

func IncrementAICachedTokens(ctx context.Context, sessionID, tokens int64) error {
	_, err := db.ExecContext(ctx, `UPDATE ai_session_meta
SET cached_input_tokens=cached_input_tokens + ? WHERE session_id = ?`, tokens, sessionID)
	return err
}

type AIMessageUsage struct {
	SessionID             int64
	Provider              string
	Model                 string
	InputTokens           int64
	OutputTokens          int64
	CachedInputTokens     int64
	InputMessageCount     int64
	InputFirstMsgID       int64
	InputLastMsgID        int64
	GeminiCacheExpireTime int64
}

type AIAssistantPayload struct {
	MsgID    int64
	Provider string
	Format   string
	Payload  []byte
}

type AISessionRuntimeState struct {
	GeminiInteractionID    string
	WindowStartMsgID       int64
	GeminiCacheName        string
	GeminiCacheExpireTime  int64
	GeminiCacheStartMsgID  int64
	GeminiCacheEndMsgID    int64
	GeminiCacheTokenCount  int64
	GeminiCacheFingerprint string
	HistoryRebuildLossy    bool
}

func GetAISessionRuntimeState(ctx context.Context, sessionID int64) (state AISessionRuntimeState, err error) {
	return getAISessionRuntimeState(ctx, db, sessionID)
}

func getAISessionRuntimeState(ctx context.Context, database *sql.DB, sessionID int64) (state AISessionRuntimeState, err error) {
	var interactionID, cacheName, cacheFingerprint sql.NullString
	var windowStart, cacheExpire, cacheStart, cacheEnd sql.NullInt64
	err = database.QueryRowContext(ctx, `SELECT gemini_interaction_id, window_start_msg_id,
gemini_cache_name, gemini_cache_expire_time, gemini_cache_start_msg_id, gemini_cache_end_msg_id,
gemini_cache_token_count, gemini_cache_fingerprint, history_rebuild_lossy
FROM ai_session_meta WHERE session_id = ?`, sessionID).Scan(
		&interactionID, &windowStart, &cacheName, &cacheExpire, &cacheStart, &cacheEnd,
		&state.GeminiCacheTokenCount, &cacheFingerprint, &state.HistoryRebuildLossy)
	if err != nil {
		return state, err
	}
	if interactionID.Valid {
		state.GeminiInteractionID = interactionID.String
	}
	if windowStart.Valid {
		state.WindowStartMsgID = windowStart.Int64
	}
	if cacheName.Valid {
		state.GeminiCacheName = cacheName.String
	}
	if cacheExpire.Valid {
		state.GeminiCacheExpireTime = cacheExpire.Int64
	}
	if cacheStart.Valid {
		state.GeminiCacheStartMsgID = cacheStart.Int64
	}
	if cacheEnd.Valid {
		state.GeminiCacheEndMsgID = cacheEnd.Int64
	}
	if cacheFingerprint.Valid {
		state.GeminiCacheFingerprint = cacheFingerprint.String
	}
	return state, nil
}

func SetAISessionRuntimeState(ctx context.Context, executor AIResponseExecutor, sessionID int64,
	interactionID string, windowStartMsgID int64,
) error {
	return setAISessionRuntimeState(ctx, executor, sessionID, AISessionRuntimeState{
		GeminiInteractionID: interactionID, WindowStartMsgID: windowStartMsgID,
	}, false)
}

func SetAISessionRuntimeStateFull(ctx context.Context, executor AIResponseExecutor, sessionID int64,
	state AISessionRuntimeState,
) error {
	return setAISessionRuntimeState(ctx, executor, sessionID, state, true)
}

func setAISessionRuntimeState(ctx context.Context, executor AIResponseExecutor, sessionID int64,
	state AISessionRuntimeState, includeCache bool,
) error {
	var interactionValue any
	if state.GeminiInteractionID != "" {
		interactionValue = state.GeminiInteractionID
	}
	var windowValue any
	if state.WindowStartMsgID != 0 {
		windowValue = state.WindowStartMsgID
	}
	query := `UPDATE ai_session_meta
SET gemini_interaction_id = ?, window_start_msg_id = ?, history_rebuild_lossy = 0 WHERE session_id = ?`
	args := []any{interactionValue, windowValue, sessionID}
	if includeCache {
		var cacheName, cacheExpire, cacheStart, cacheEnd, cacheFingerprint any
		if state.GeminiCacheName != "" {
			cacheName = state.GeminiCacheName
		}
		if state.GeminiCacheExpireTime != 0 {
			cacheExpire = state.GeminiCacheExpireTime
		}
		if state.GeminiCacheStartMsgID != 0 {
			cacheStart = state.GeminiCacheStartMsgID
		}
		if state.GeminiCacheEndMsgID != 0 {
			cacheEnd = state.GeminiCacheEndMsgID
		}
		if state.GeminiCacheFingerprint != "" {
			cacheFingerprint = state.GeminiCacheFingerprint
		}
		query = `UPDATE ai_session_meta SET gemini_interaction_id = ?, window_start_msg_id = ?,
gemini_cache_name = ?, gemini_cache_expire_time = ?, gemini_cache_start_msg_id = ?,
gemini_cache_end_msg_id = ?, gemini_cache_token_count = ?, gemini_cache_fingerprint = ?,
history_rebuild_lossy = 0 WHERE session_id = ?`
		args = []any{interactionValue, windowValue, cacheName, cacheExpire, cacheStart, cacheEnd,
			state.GeminiCacheTokenCount, cacheFingerprint, sessionID}
	}
	result, err := executor.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("update AI session runtime state: session %d not found", sessionID)
	}
	return nil
}

type AIResponseExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func SetAIMessageUsage(ctx context.Context, sessionID, msgID, chatID int64, provider, model string,
	inputTokens, outputTokens, cachedInputTokens int64,
) error {
	return UpsertAIMessageResponse(ctx, db, sessionID, msgID, chatID, AIMessageUsage{
		Provider: provider, Model: model, InputTokens: inputTokens, OutputTokens: outputTokens,
		CachedInputTokens: cachedInputTokens,
	}, "", nil)
}

func UpsertAIMessageResponse(ctx context.Context, executor AIResponseExecutor, sessionID, msgID, chatID int64,
	usage AIMessageUsage, payloadFormat string, payload []byte,
) error {
	var format any
	if payloadFormat != "" {
		format = payloadFormat
	}
	_, err := executor.ExecContext(ctx, `INSERT INTO ai_message_meta(
session_id, msg_id, chat_id, provider, model, input_tokens, output_tokens, cached_input_tokens,
input_message_count, input_first_msg_id, input_last_msg_id, assistant_payload, assistant_payload_format)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, msg_id) DO UPDATE SET
chat_id=excluded.chat_id, provider=excluded.provider, model=excluded.model,
input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
cached_input_tokens=excluded.cached_input_tokens,
input_message_count=excluded.input_message_count,
input_first_msg_id=excluded.input_first_msg_id,
input_last_msg_id=excluded.input_last_msg_id,
assistant_payload=COALESCE(excluded.assistant_payload, ai_message_meta.assistant_payload),
assistant_payload_format=COALESCE(excluded.assistant_payload_format, ai_message_meta.assistant_payload_format)`,
		sessionID, msgID, chatID, usage.Provider, usage.Model, usage.InputTokens, usage.OutputTokens,
		usage.CachedInputTokens, usage.InputMessageCount, usage.InputFirstMsgID, usage.InputLastMsgID,
		payload, format)
	return err
}

func GetAISessionAssistantPayloads(ctx context.Context, sessionID int64) (map[int64]AIAssistantPayload, error) {
	rows, err := db.QueryContext(ctx, `SELECT msg_id, provider, assistant_payload_format, assistant_payload
FROM ai_message_meta
WHERE session_id = ? AND assistant_payload IS NOT NULL AND assistant_payload_format IS NOT NULL`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]AIAssistantPayload)
	for rows.Next() {
		var item AIAssistantPayload
		if err = rows.Scan(&item.MsgID, &item.Provider, &item.Format, &item.Payload); err != nil {
			return nil, err
		}
		result[item.MsgID] = item
	}
	return result, rows.Err()
}

func GetAIMessageUsage(ctx context.Context, chatID, msgID int64) (usage AIMessageUsage, err error) {
	return getAIMessageUsage(ctx, db, chatID, msgID)
}

func HasAIMessageResponse(ctx context.Context, chatID, msgID int64) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM ai_message_meta WHERE chat_id = ? AND msg_id = ?)`, chatID, msgID).Scan(&exists)
	return exists, err
}

func MarkMessageAsUserInput(ctx context.Context, chatID, msgID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE gemini_contents SET role = 'user'
WHERE chat_id = ? AND msg_id = ? AND NOT EXISTS (
    SELECT 1 FROM ai_message_meta WHERE chat_id = ? AND msg_id = ?)`,
		chatID, msgID, chatID, msgID)
	return err
}

func getAIMessageUsage(ctx context.Context, database *sql.DB, chatID, msgID int64) (usage AIMessageUsage, err error) {
	err = database.QueryRowContext(ctx, `SELECT m.session_id, m.provider, m.model, m.input_tokens,
m.output_tokens, m.cached_input_tokens, m.input_message_count, m.input_first_msg_id,
m.input_last_msg_id, COALESCE(s.gemini_cache_expire_time, 0)
FROM ai_message_meta AS m
LEFT JOIN ai_session_meta AS s ON s.session_id = m.session_id
WHERE m.chat_id = ? AND m.msg_id = ?`, chatID, msgID).Scan(
		&usage.SessionID, &usage.Provider, &usage.Model, &usage.InputTokens, &usage.OutputTokens,
		&usage.CachedInputTokens, &usage.InputMessageCount, &usage.InputFirstMsgID, &usage.InputLastMsgID,
		&usage.GeminiCacheExpireTime)
	return
}
