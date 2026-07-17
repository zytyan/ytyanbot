package genbot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	g "main/globalcfg"
	"main/globalcfg/aiq"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func useSystemPromptTestDB(t *testing.T) *aiq.Queries {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	schema, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "sql", "schema_ai_v2.sql"))
	require.NoError(t, err)
	database, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "ai.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	_, err = database.Exec(string(schema))
	require.NoError(t, err)
	queries, err := aiq.Prepare(context.Background(), database)
	require.NoError(t, err)

	oldQueries := g.AIQ
	g.AIQ = queries
	gMu.Lock()
	oldCache := sysPromptReplacerCache
	sysPromptReplacerCache = make(map[geminiTopic]*Replacer)
	gMu.Unlock()
	t.Cleanup(func() {
		gMu.Lock()
		sysPromptReplacerCache = oldCache
		gMu.Unlock()
		g.AIQ = oldQueries
		require.NoError(t, queries.Close())
		require.NoError(t, database.Close())
	})
	return queries
}

func systemPromptTestContext(bot *gotgbot.Bot, text string, reply *gotgbot.Message, topicID int64) *ext.Context {
	msg := &gotgbot.Message{
		MessageId:      10,
		Chat:           gotgbot.Chat{Id: -1001, Type: "supergroup"},
		Text:           text,
		ReplyToMessage: reply,
	}
	if topicID != 0 {
		msg.IsTopicMessage = true
		msg.MessageThreadId = topicID
	}
	return ext.NewContext(bot, &gotgbot.Update{UpdateId: 1, Message: msg}, nil)
}

func TestSystemPromptFromMessage(t *testing.T) {
	replied := &gotgbot.Message{Text: "prompt from reply"}
	require.Equal(t, "inline prompt", systemPromptFromMessage(&gotgbot.Message{
		Text: "/sysprompt inline prompt", ReplyToMessage: replied,
	}))
	require.Equal(t, "prompt from reply", systemPromptFromMessage(&gotgbot.Message{
		Text: "/sysprompt", ReplyToMessage: replied,
	}))
	require.Equal(t, "caption prompt", systemPromptFromMessage(&gotgbot.Message{
		Text: "/sysprompt", ReplyToMessage: &gotgbot.Message{Caption: "caption prompt"},
	}))
	require.Empty(t, systemPromptFromMessage(&gotgbot.Message{
		Text: "/sysprompt", ReplyToMessage: &gotgbot.Message{Text: "  \n"},
	}))
}

func TestUpdateSystemPromptFromReplyAndInvalidateCache(t *testing.T) {
	queries := useSystemPromptTestDB(t)
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	ctx := systemPromptTestContext(bot, "/sysprompt", &gotgbot.Message{Text: "reply prompt %CHAT_NAME%"}, 23)
	topic := newTopic(ctx.EffectiveMessage)
	stale := NewReplacer("stale")
	gMu.Lock()
	sysPromptReplacerCache[topic] = &stale
	gMu.Unlock()

	require.NoError(t, UpdateGeminiSysPrompt(bot, ctx))
	prompt, err := queries.GetAISystemPrompt(context.Background(), -1001, 23)
	require.NoError(t, err)
	require.Equal(t, "reply prompt %CHAT_NAME%", prompt)
	require.Equal(t, "sendMessage", client.method)
	require.Equal(t, "成功设置系统提示词:\nreply prompt %CHAT_NAME%", client.params["text"])
	gMu.Lock()
	_, cached := sysPromptReplacerCache[topic]
	gMu.Unlock()
	require.False(t, cached)
}

func TestSystemPromptUsesCanonicalNonTopicID(t *testing.T) {
	queries := useSystemPromptTestDB(t)
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	ctx := systemPromptTestContext(bot, "/sysprompt canonical", nil, 0)
	ctx.EffectiveMessage.MessageThreadId = 99

	require.NoError(t, UpdateGeminiSysPrompt(bot, ctx))
	prompt, err := queries.GetAISystemPrompt(context.Background(), -1001, 0)
	require.NoError(t, err)
	require.Equal(t, "canonical", prompt)
	_, err = queries.GetAISystemPrompt(context.Background(), -1001, 99)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestEmptySavedSystemPromptFallsBackToDefault(t *testing.T) {
	queries := useSystemPromptTestDB(t)
	require.NoError(t, queries.UpsertAISystemPrompt(context.Background(), -1001, 0, "", 1))
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	ctx := systemPromptTestContext(bot, "/get_sysprompt", nil, 0)

	require.NoError(t, GetGeminiSysPrompt(bot, ctx))
	require.Equal(t, gDefaultSysPrompt, client.params["text"])
	replacer, err := getSysPrompt(ctx.EffectiveMessage)
	require.NoError(t, err)
	require.Same(t, &geminiSysPromptReplacer, replacer)
}

func TestResetSystemPromptDeletesRecordAndInvalidatesCache(t *testing.T) {
	queries := useSystemPromptTestDB(t)
	require.NoError(t, queries.UpsertAISystemPrompt(context.Background(), -1001, 7, "custom", 1))
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	ctx := systemPromptTestContext(bot, "/reset_sysprompt", nil, 7)
	topic := newTopic(ctx.EffectiveMessage)
	custom := NewReplacer("custom")
	gMu.Lock()
	sysPromptReplacerCache[topic] = &custom
	gMu.Unlock()

	require.NoError(t, ResetGeminiSysPrompt(bot, ctx))
	_, err := queries.GetAISystemPrompt(context.Background(), -1001, 7)
	require.ErrorIs(t, err, sql.ErrNoRows)
	gMu.Lock()
	_, cached := sysPromptReplacerCache[topic]
	gMu.Unlock()
	require.False(t, cached)
}
