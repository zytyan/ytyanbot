package aiq

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func openV2TestQueries(t *testing.T) (*sql.DB, *Queries) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	schemaPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "sql", "schema_ai_v2.sql")
	schema, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	databasePath := filepath.Join(t.TempDir(), "ai-v2.db")
	database, err := sql.Open("sqlite3", "file:"+databasePath+"?_foreign_keys=on&_busy_timeout=5000")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	_, err = database.Exec(string(schema))
	require.NoError(t, err)
	queries, err := Prepare(context.Background(), database)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, queries.Close())
		require.NoError(t, database.Close())
	})
	return database, queries
}

func TestV2QueriesSupportSharedMessagesAndRunLifecycle(t *testing.T) {
	database, queries := openV2TestQueries(t)
	ctx := context.Background()
	settings, err := queries.UpsertAIChatSettings(ctx, UpsertAIChatSettingsParams{
		ChatID: -1001, DefaultProvider: "gemini", DefaultModel: "gemini-test",
		ShowUsage: 0, UpdatedAt: 100,
	})
	require.NoError(t, err)
	require.Equal(t, "gemini", settings.DefaultProvider)
	settings, err = queries.ToggleAIChatSettingsUsage(ctx, 101, -1001)
	require.NoError(t, err)
	require.Equal(t, int64(1), settings.ShowUsage)

	require.NoError(t, queries.InsertAIMessage(ctx, InsertAIMessageParams{
		ChatID: -1001, MsgID: 10, SentAt: 100, UserID: 7, Username: "user",
		MsgType: "text", Text: sql.NullString{String: "hello", Valid: true},
	}))
	first, err := queries.CreateAISession(ctx, CreateAISessionParams{
		ChatID: -1001, TopicID: sql.NullInt64{Int64: 44, Valid: true}, ChatName: "chat",
		ChatType: "supergroup", Provider: "gemini", Model: "gemini-test", CreatedAt: 100, UpdatedAt: 100,
	})
	require.NoError(t, err)
	second, err := queries.CreateAISession(ctx, CreateAISessionParams{
		ChatID: -1001, TopicID: sql.NullInt64{Int64: 44, Valid: true}, ChatName: "chat",
		ChatType: "supergroup", Provider: "deepseek", Model: "deepseek-test", CreatedAt: 100, UpdatedAt: 100,
	})
	require.NoError(t, err)
	for _, session := range []AiSession{first, second} {
		require.NoError(t, queries.AddAISessionMessage(ctx, AddAISessionMessageParams{
			SessionID: session.ID, Position: 0, ChatID: -1001, MsgID: 10, Role: "user",
			ContextOnly: 0,
		}))
		messages, listErr := queries.ListAISessionMessages(ctx, session.ID)
		require.NoError(t, listErr)
		require.Len(t, messages, 1)
		require.Equal(t, int64(10), messages[0].MsgID)
	}

	run, err := queries.CreateAIRun(ctx, CreateAIRunParams{
		SessionID: first.ID, RequestChatID: -1001, RequestMsgID: 10,
		Provider: "gemini", Model: "gemini-test", RequestedAt: 101,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", run.Status)
	idempotent, err := queries.CreateAIRun(ctx, CreateAIRunParams{
		SessionID: first.ID, RequestChatID: -1001, RequestMsgID: 10,
		Provider: "gemini", Model: "gemini-test", RequestedAt: 999,
	})
	require.NoError(t, err)
	require.Equal(t, run.ID, idempotent.ID)
	require.Equal(t, run.RequestedAt, idempotent.RequestedAt)

	rows, err := queries.MarkAIRunGenerated(ctx, MarkAIRunGeneratedParams{
		CompletedAt:            sql.NullInt64{Int64: 102, Valid: true},
		InputTokens:            sql.NullInt64{Int64: 12, Valid: true},
		OutputTokens:           sql.NullInt64{Int64: 3, Valid: true},
		CachedInputTokens:      sql.NullInt64{Int64: 8, Valid: true},
		InputMessageCount:      sql.NullInt64{Int64: 1, Valid: true},
		InputFirstMsgID:        sql.NullInt64{Int64: 10, Valid: true},
		InputLastMsgID:         sql.NullInt64{Int64: 10, Valid: true},
		ResponseText:           sql.NullString{String: "answer", Valid: true},
		AssistantPayload:       []byte(`{"role":"model"}`),
		AssistantPayloadFormat: sql.NullString{String: "gemini-content-v1", Valid: true},
		CandidateStateJson:     sql.NullString{String: `{"interaction_id":"next"}`, Valid: true},
		RunID:                  run.ID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	rows, err = queries.MarkAIRunGenerated(ctx, MarkAIRunGeneratedParams{RunID: run.ID})
	require.NoError(t, err)
	require.Zero(t, rows, "a generated run cannot account usage twice")

	require.NoError(t, queries.InsertAIMessage(ctx, InsertAIMessageParams{
		ChatID: -1001, MsgID: 11, SentAt: 103, UserID: 99, Username: "bot",
		MsgType: "text", Text: sql.NullString{String: "answer", Valid: true},
	}))
	require.NoError(t, queries.AddAISessionMessage(ctx, AddAISessionMessageParams{
		SessionID: first.ID, Position: 1, ChatID: -1001, MsgID: 11, Role: "model",
	}))
	rows, err = queries.MarkAIRunDelivered(ctx,
		sql.NullInt64{Int64: -1001, Valid: true}, sql.NullInt64{Int64: 11, Valid: true}, run.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, queries.IncrementAISessionUsage(ctx, IncrementAISessionUsageParams{
		InputTokens: 12, OutputTokens: 3, CachedInputTokens: 8, UpdatedAt: 103, SessionID: first.ID,
	}))
	delivered, err := queries.GetAIRunByResponse(ctx,
		sql.NullInt64{Int64: -1001, Valid: true}, sql.NullInt64{Int64: 11, Valid: true})
	require.NoError(t, err)
	require.Equal(t, "delivered", delivered.Status)
	require.Equal(t, int64(12), delivered.InputTokens.Int64)
	savedSession, err := queries.GetAISession(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, int64(12), savedSession.TotalInputTokens)

	require.NoError(t, queries.UpsertAISessionProviderState(ctx, UpsertAISessionProviderStateParams{
		SessionID: first.ID, Provider: "gemini", StateVersion: 1,
		StateJson: `{"interaction_id":"next"}`, UpdatedAt: 103,
	}))
	state, err := queries.GetAISessionProviderState(ctx, first.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"interaction_id":"next"}`, state.StateJson)

	_, err = database.Exec(`INSERT INTO ai_session_provider_state(session_id, provider, state_version, state_json, updated_at)
VALUES (?, 'gemini', 1, 'not-json', 1)`, second.ID)
	require.ErrorContains(t, err, "CHECK constraint failed")
}

func TestV2MediaPromptAndConstraints(t *testing.T) {
	database, queries := openV2TestQueries(t)
	ctx := context.Background()
	require.NoError(t, queries.InsertAIMessage(ctx, InsertAIMessageParams{
		ChatID: 1, MsgID: 2, SentAt: 3, UserID: 4, Username: "user", MsgType: "photo",
	}))
	hash := strings.Repeat("a", 64)
	require.NoError(t, queries.InsertMediaObject(ctx, InsertMediaObjectParams{
		Sha256: hash, RelativePath: "sha256/aa/" + hash, ByteSize: 5,
		MimeType: "image/jpeg", CreatedAt: 3,
	}))
	require.NoError(t, queries.AddAIMessageMedia(ctx, AddAIMessageMediaParams{
		ChatID: 1, MsgID: 2, Ordinal: 0, MediaSha256: hash, MediaKind: "photo",
	}))
	media, err := queries.ListAIMessageMedia(ctx, 1, 2)
	require.NoError(t, err)
	require.Len(t, media, 1)
	require.Equal(t, hash, media[0].Sha256)
	hashes, err := queries.ListReferencedMediaHashes(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{hash}, hashes)

	_, err = database.Exec(`DELETE FROM media_objects WHERE sha256=?`, hash)
	require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
	_, err = database.Exec(`INSERT INTO media_objects(sha256, relative_path, byte_size, mime_type, created_at)
VALUES (?, 'bad', -1, 'image/jpeg', 1)`, strings.Repeat("b", 64))
	require.ErrorContains(t, err, "CHECK constraint failed")

	require.NoError(t, queries.UpsertAISystemPrompt(ctx, 1, 9, "prompt", 10))
	prompt, err := queries.GetAISystemPrompt(ctx, 1, 9)
	require.NoError(t, err)
	require.Equal(t, "prompt", prompt)
}
