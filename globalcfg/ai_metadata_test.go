package g

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func openAIMetadataTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "ai.db"))
	require.NoError(t, err)
	database.SetMaxOpenConns(8)
	_, err = database.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE gemini_sessions (id INTEGER PRIMARY KEY);
CREATE TABLE gemini_contents (
    session_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    msg_id INTEGER NOT NULL,
    PRIMARY KEY(session_id, msg_id),
    UNIQUE(chat_id, msg_id)
);
CREATE TABLE ai_chat_models (
    chat_id INTEGER PRIMARY KEY,
    model TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE ai_message_meta (
    session_id INTEGER NOT NULL,
    msg_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    PRIMARY KEY(session_id, msg_id)
) WITHOUT ROWID;
CREATE TABLE gemini_system_prompt (
    chat_id INTEGER NOT NULL,
    thread_id INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    PRIMARY KEY(chat_id, thread_id)
) WITHOUT ROWID;
INSERT INTO gemini_sessions(id) VALUES (7);
INSERT INTO gemini_contents(session_id, chat_id, msg_id) VALUES (7, -1001, 99);
INSERT INTO ai_chat_models(chat_id, model) VALUES (-1001, 'old-model');
INSERT INTO ai_message_meta(session_id, msg_id, provider, model)
VALUES (7, 99, 'gemini', 'old-model');
INSERT INTO gemini_system_prompt(chat_id, thread_id, prompt)
VALUES (-1001, 0, 'time=%DATETIME_TZ% sender=%SENDER_NAME% quote=%QUOTE%');`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func TestAIMetadataMigrationAndUsagePersistence(t *testing.T) {
	database := openAIMetadataTestDB(t)
	require.NoError(t, initAIMetadataSchema(database))

	enabled, err := getAIChatUsageEnabled(context.Background(), database, -1001)
	require.NoError(t, err)
	require.False(t, enabled)

	enabled, err = toggleAIChatUsage(context.Background(), database, -1001, "fallback")
	require.NoError(t, err)
	require.True(t, enabled)
	var model string
	require.NoError(t, database.QueryRow(`SELECT model FROM ai_chat_models WHERE chat_id=-1001`).Scan(&model))
	require.Equal(t, "old-model", model)

	payload := []byte(`{"role":"assistant","reasoning_content":"think","content":"answer"}`)
	require.NoError(t, UpsertAIMessageResponse(context.Background(), database, 7, 99, -1001,
		AIMessageUsage{Provider: "deepseek", Model: "deepseek-v4-flash", InputTokens: 27014,
			OutputTokens: 32, CachedInputTokens: 27008, InputMessageCount: 174,
			InputFirstMsgID: 100, InputLastMsgID: 274}, "deepseek-message-v1", payload))
	usage, err := getAIMessageUsage(context.Background(), database, -1001, 99)
	require.NoError(t, err)
	require.Equal(t, AIMessageUsage{
		SessionID: 7, Provider: "deepseek", Model: "deepseek-v4-flash", InputTokens: 27014,
		OutputTokens: 32, CachedInputTokens: 27008, InputMessageCount: 174,
		InputFirstMsgID: 100, InputLastMsgID: 274,
	}, usage)
	var payloadFormat string
	var savedPayload []byte
	require.NoError(t, database.QueryRow(`SELECT assistant_payload_format, assistant_payload
FROM ai_message_meta WHERE session_id=7 AND msg_id=99`).Scan(&payloadFormat, &savedPayload))
	require.Equal(t, "deepseek-message-v1", payloadFormat)
	require.Equal(t, payload, savedPayload)
	var migratedPrompt string
	require.NoError(t, database.QueryRow(`SELECT prompt FROM gemini_system_prompt
WHERE chat_id=-1001 AND thread_id=0`).Scan(&migratedPrompt))
	require.NotContains(t, migratedPrompt, "%DATETIME_TZ%")
	require.NotContains(t, migratedPrompt, "%SENDER_NAME%")
	require.NotContains(t, migratedPrompt, "%QUOTE%")
	require.Contains(t, migratedPrompt, "最新用户消息头")
	require.Contains(t, migratedPrompt, "不可用")

	_, err = database.Exec(`INSERT INTO ai_session_meta(session_id, provider, model)
VALUES (7, 'gemini', 'gemini-3-flash-preview')`)
	require.NoError(t, err)
	require.NoError(t, SetAISessionRuntimeState(context.Background(), database, 7, "int-test", 51))
	runtimeState, err := getAISessionRuntimeState(context.Background(), database, 7)
	require.NoError(t, err)
	require.Equal(t, AISessionRuntimeState{GeminiInteractionID: "int-test", WindowStartMsgID: 51}, runtimeState)

	cacheState := AISessionRuntimeState{
		WindowStartMsgID: 51, GeminiCacheName: "cachedContents/cache-7",
		GeminiCacheExpireTime: 1_800_000_000, GeminiCacheStartMsgID: 51,
		GeminiCacheEndMsgID: 88, GeminiCacheTokenCount: 5000,
		GeminiCacheFingerprint: "fingerprint",
	}
	require.NoError(t, SetAISessionRuntimeStateFull(context.Background(), database, 7, cacheState))
	runtimeState, err = getAISessionRuntimeState(context.Background(), database, 7)
	require.NoError(t, err)
	require.Equal(t, cacheState, runtimeState)
	usage, err = getAIMessageUsage(context.Background(), database, -1001, 99)
	require.NoError(t, err)
	require.Equal(t, cacheState.GeminiCacheExpireTime, usage.GeminiCacheExpireTime,
		"historical usage queries must expose the session's current cache expiration")

	require.NoError(t, changeAISessionModel(context.Background(), database, 7,
		"deepseek", "deepseek-v4-flash"))
	runtimeState, err = getAISessionRuntimeState(context.Background(), database, 7)
	require.NoError(t, err)
	require.Equal(t, AISessionRuntimeState{WindowStartMsgID: 51, HistoryRebuildLossy: true}, runtimeState,
		"model changes must clear provider-specific explicit cache state")
	var provider, sessionModel string
	require.NoError(t, database.QueryRow(`SELECT provider, model FROM ai_session_meta WHERE session_id=7`).
		Scan(&provider, &sessionModel))
	require.Equal(t, "deepseek", provider)
	require.Equal(t, "deepseek-v4-flash", sessionModel)

	require.NoError(t, SetAISessionRuntimeState(context.Background(), database, 7, "int-after-switch", 51))
	runtimeState, err = getAISessionRuntimeState(context.Background(), database, 7)
	require.NoError(t, err)
	require.Equal(t, AISessionRuntimeState{GeminiInteractionID: "int-after-switch", WindowStartMsgID: 51}, runtimeState)

	tx, err := database.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, UpsertAIMessageResponse(context.Background(), tx, 7, 99, -1001,
		AIMessageUsage{Provider: "gemini", Model: "temporary", InputTokens: 1},
		"gemini-content-v1", []byte(`{"role":"model"}`)))
	require.NoError(t, tx.Rollback())
	usage, err = getAIMessageUsage(context.Background(), database, -1001, 99)
	require.NoError(t, err)
	require.Equal(t, "deepseek-v4-flash", usage.Model)
}

func TestToggleAIChatUsageConcurrentIsolation(t *testing.T) {
	database := openAIMetadataTestDB(t)
	require.NoError(t, initAIMetadataSchema(database))

	const toggles = 20
	errCh := make(chan error, toggles)
	var wg sync.WaitGroup
	for i := 0; i < toggles; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := toggleAIChatUsage(context.Background(), database, -2002, "gemini")
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	enabled, err := getAIChatUsageEnabled(context.Background(), database, -2002)
	require.NoError(t, err)
	require.Equal(t, toggles%2 == 1, enabled, fmt.Sprintf("after %d atomic toggles", toggles))

	other, err := getAIChatUsageEnabled(context.Background(), database, -2003)
	require.NoError(t, err)
	require.False(t, other)
}
