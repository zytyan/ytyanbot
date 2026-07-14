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
INSERT INTO gemini_sessions(id) VALUES (7);
INSERT INTO gemini_contents(session_id, chat_id, msg_id) VALUES (7, -1001, 99);
INSERT INTO ai_chat_models(chat_id, model) VALUES (-1001, 'old-model');
INSERT INTO ai_message_meta(session_id, msg_id, provider, model)
VALUES (7, 99, 'gemini', 'old-model');`)
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

	require.NoError(t, setAIMessageUsage(context.Background(), database, 7, 99, -1001,
		"deepseek", "deepseek-v4-flash", 27014, 32, 27008))
	usage, err := getAIMessageUsage(context.Background(), database, -1001, 99)
	require.NoError(t, err)
	require.Equal(t, AIMessageUsage{
		Provider: "deepseek", Model: "deepseek-v4-flash", InputTokens: 27014,
		OutputTokens: 32, CachedInputTokens: 27008,
	}, usage)
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
