package genbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	g "main/globalcfg"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/stretchr/testify/require"
)

func testContent(msgType, text string) q.GeminiContent {
	return q.GeminiContent{
		SessionID: 1,
		MsgID:     2,
		Role:      "user",
		SentTime:  q.UnixTime{Time: time.Unix(123, 0)},
		Username:  "tester",
		MsgType:   msgType,
		Text:      sql.NullString{String: text, Valid: text != ""},
	}
}

func TestDeepSeekMediaConversion(t *testing.T) {
	photo, ok := deepSeekContent(ptr(testContent("photo", "caption")), nil)
	require.True(t, ok)
	require.Equal(t, "[ tester 1970-01-01 08:02:03 ]\ncaption\n[图片]", photo.Content)
	require.NotContains(t, photo.Content, "-start-label-")
	require.NotContains(t, photo.Content, "id:")
	require.NotContains(t, photo.Content, "type:")
	require.NotContains(t, photo.Content, "reply:")
	require.NotContains(t, photo.Content, "quote:")

	sticker, ok := deepSeekContent(ptr(testContent("sticker", "🙂")), nil)
	require.True(t, ok)
	require.Contains(t, sticker.Content, "🙂")

	_, ok = deepSeekContent(ptr(testContent("video", "")), nil)
	require.False(t, ok)
	videoCaption, ok := deepSeekContent(ptr(testContent("video", "only caption")), nil)
	require.True(t, ok)
	require.Contains(t, videoCaption.Content, "only caption")
	require.NotContains(t, videoCaption.Content, "[视频]")
}

func TestDeepSeekPureVideoRejected(t *testing.T) {
	session := &GeminiSession{TmpContents: []q.GeminiContent{testContent("video", "")}}
	_, err := session.ToDeepSeekMessages("system")
	require.ErrorIs(t, err, ErrDeepSeekVideoOnly)
}

func TestDeepSeekLossyHistoryUsesPortableText(t *testing.T) {
	photo := testContent("photo", "caption")
	photo.MsgID = 1
	answer := testContent("text", "portable answer")
	answer.MsgID = 2
	answer.Role = genai.RoleModel
	session := &GeminiSession{
		Contents: []q.GeminiContent{photo, answer}, HistoryRebuildLossy: true,
		AssistantPayloads: map[int64]g.AIAssistantPayload{2: {
			MsgID: 2, Provider: ProviderDeepSeek, Format: PayloadFormatDeepSeekMessage,
			Payload: []byte(`{"role":"assistant","content":"raw answer","reasoning_content":"encrypted thought","tool_calls":[{"id":"call"}]}`),
		}},
	}
	messages, err := session.ToDeepSeekMessages("system")
	require.NoError(t, err)
	require.Len(t, messages, 3)
	require.Contains(t, messages[1].Content, "[图片]")
	require.Equal(t, "portable answer", messages[2].Content)
	require.Empty(t, messages[2].ReasoningContent)
	require.Empty(t, messages[2].ToolCalls)
}

func TestCallDeepSeek(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"role":"assistant","reasoning_content":"thinking","content":"ok","tool_calls":[{"id":"call-1","type":"function"}],"future_field":{"x":1}}}],"usage":{"prompt_tokens":20,"prompt_cache_hit_tokens":12,"prompt_cache_miss_tokens":8,"completion_tokens":3,"completion_tokens_details":{"reasoning_tokens":1}}}`)),
		}, nil
	})}

	result, err := callDeepSeek(context.Background(), client, "https://example.test", "secret", deepSeekRequest{
		Model: ModelDeepSeekFlash, Messages: []deepSeekMessage{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", result.DisplayText)
	require.Equal(t, int64(12), result.Usage.CachedInputTokens)
	require.Equal(t, int64(1), result.Usage.ThinkingTokens)
	require.Equal(t, PayloadFormatDeepSeekMessage, result.AssistantPayloadFormat)
	var assistant deepSeekMessage
	require.NoError(t, json.Unmarshal(result.AssistantPayload, &assistant))
	require.Equal(t, "thinking", assistant.ReasoningContent)
	require.JSONEq(t, `[{"id":"call-1","type":"function"}]`, string(assistant.ToolCalls))
	require.Contains(t, string(result.AssistantPayload), `"future_field":{"x":1}`)

	replayed, ok := deepSeekContent(&q.GeminiContent{MsgID: 8, Role: "model"}, map[int64]g.AIAssistantPayload{
		8: {MsgID: 8, Provider: ProviderDeepSeek, Format: result.AssistantPayloadFormat, Payload: result.AssistantPayload},
	})
	require.True(t, ok)
	require.Equal(t, assistant, replayed)
	replayedJSON, err := json.Marshal(replayed)
	require.NoError(t, err)
	require.Contains(t, string(replayedJSON), `"future_field":{"x":1}`)
}

func TestCallDeepSeekErrorDoesNotExposeKey(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid credentials"}}`)),
		}, nil
	})}
	_, err := callDeepSeek(context.Background(), client, "https://example.test", "secret-value", deepSeekRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid credentials")
	require.NotContains(t, err.Error(), "secret-value")
}

func TestStablePromptReplacesDeprecatedDynamicVariables(t *testing.T) {
	r := NewReplacer("time=%DATETIME_TZ% chat=%CHAT_NAME% quote=%QUOTE%")
	ctx := &ReplaceCtx{
		Stable: true,
		Now:    time.Unix(123, 0),
		Bot:    &gotgbot.Bot{},
		Msg:    &gotgbot.Message{Chat: gotgbot.Chat{Title: "group"}},
	}
	got := r.Replace(ctx)
	require.Contains(t, got, "最新用户消息头")
	require.Contains(t, got, "不可用")
	require.Contains(t, got, "group")
}

func TestGeminiCompactPartsAndRawAssistantReplay(t *testing.T) {
	photo := testContent("photo", "caption")
	photo.Blob = []byte("image")
	photo.MimeType = sql.NullString{String: "image/jpeg", Valid: true}
	converted := databaseContentToGenaiPart(&photo, nil)
	require.Equal(t, "user", converted.Role)
	require.Len(t, converted.Parts, 2)
	require.Equal(t, "[ tester 1970-01-01 08:02:03 ]\ncaption", converted.Parts[0].Text)
	require.Equal(t, []byte("image"), converted.Parts[1].InlineData.Data)

	rawContent := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
		{Text: "private thought", Thought: true, ThoughtSignature: []byte("sig-1")},
		{ExecutableCode: &genai.ExecutableCode{Code: "print(1)", Language: genai.LanguagePython}},
		{Text: "final answer", ThoughtSignature: []byte("sig-2")},
	}}
	result, err := resultFromGeminiResponse(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: rawContent}},
	})
	require.NoError(t, err)
	require.Equal(t, "final answer", result.DisplayText)
	require.NotContains(t, result.DisplayText, "private thought")
	require.Equal(t, PayloadFormatGeminiContent, result.AssistantPayloadFormat)

	var saved genai.Content
	require.NoError(t, json.Unmarshal(result.AssistantPayload, &saved))
	require.Equal(t, rawContent, &saved)

	modelRecord := q.GeminiContent{MsgID: 9, Role: genai.RoleModel,
		Text: sql.NullString{String: "fallback", Valid: true}}
	session := &GeminiSession{
		Contents: []q.GeminiContent{photo, modelRecord},
		AssistantPayloads: map[int64]g.AIAssistantPayload{
			9: {MsgID: 9, Provider: ProviderGemini, Format: PayloadFormatGeminiContent, Payload: result.AssistantPayload},
		},
		TmpContents: []q.GeminiContent{testContent("text", "next turn")},
	}
	contents := session.ToGenaiContents()
	require.Len(t, contents, 3)
	require.Equal(t, rawContent, contents[1])
	require.Equal(t, "[ tester 1970-01-01 08:02:03 ]\nnext turn", contents[2].Parts[0].Text)

	prefixSession := &GeminiSession{
		Contents: session.Contents, AssistantPayloads: session.AssistantPayloads,
	}
	prefix := prefixSession.ToGenaiContents()
	require.Equal(t, prefix, contents[:len(prefix)])
}

func TestDamagedAssistantPayloadFallsBackToSavedBody(t *testing.T) {
	record := q.GeminiContent{MsgID: 11, Role: genai.RoleModel,
		Text: sql.NullString{String: "safe fallback", Valid: true}}
	payloads := map[int64]g.AIAssistantPayload{
		11: {MsgID: 11, Provider: ProviderGemini, Format: PayloadFormatGeminiContent, Payload: []byte("not-json")},
	}
	geminiMessage := databaseContentToGenaiPart(&record, payloads)
	require.Equal(t, "safe fallback", geminiMessage.Parts[0].Text)

	payloads[11] = g.AIAssistantPayload{MsgID: 11, Provider: ProviderDeepSeek,
		Format: PayloadFormatDeepSeekMessage, Payload: []byte("not-json")}
	deepSeekMessage, ok := deepSeekContent(&record, payloads)
	require.True(t, ok)
	require.Equal(t, "safe fallback", deepSeekMessage.Content)
}

func TestAddTgMessageWithReplyAppendsReplyFirstWithoutLegacyLabels(t *testing.T) {
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	user := &gotgbot.User{Id: 42, FirstName: "Alice", Username: "alice"}
	message := &gotgbot.Message{
		MessageId: 880102,
		Date:      123,
		Chat:      gotgbot.Chat{Id: -910101, Type: "supergroup"},
		From:      user,
		Text:      "hello",
		ReplyToMessage: &gotgbot.Message{
			MessageId: 880101,
			From:      &gotgbot.User{Id: 7, FirstName: "Bob"},
			Text:      "earlier message",
		},
		Quote: &gotgbot.TextQuote{Text: "quoted", IsManual: true},
	}
	session := &GeminiSession{GeminiSession: q.GeminiSession{ID: 5}}
	require.NoError(t, session.AddTgMessageWithReply(context.Background(), nil, message))
	require.Len(t, session.TmpContents, 2)
	require.Equal(t, int64(880101), session.TmpContents[0].MsgID)
	require.Equal(t, "earlier message", session.TmpContents[0].Text.String)
	require.Equal(t, int64(880102), session.TmpContents[1].MsgID)
	saved := session.TmpContents[1]
	require.False(t, saved.ReplyToMsgID.Valid)
	require.False(t, saved.QuotePart.Valid)
	require.Equal(t, int64(42), saved.UserID)
	require.Equal(t, "alice", saved.AtableUsername.String)
}

func TestAddTgMessageWithReplyDoesNotDuplicateActiveChain(t *testing.T) {
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	replied := &gotgbot.Message{MessageId: 881001, Date: 120,
		Chat: gotgbot.Chat{Id: -910102, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, FirstName: "Bob"}, Text: "already present"}
	current := &gotgbot.Message{MessageId: 881002, Date: 123,
		Chat: gotgbot.Chat{Id: -910102, Type: "supergroup"},
		From: &gotgbot.User{Id: 42, FirstName: "Alice"}, Text: "new", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: q.GeminiSession{ID: 6},
		Contents: []q.GeminiContent{{MsgID: replied.MessageId}}}
	require.NoError(t, session.AddTgMessageWithReply(context.Background(), nil, current))
	require.Len(t, session.TmpContents, 1)
	require.Equal(t, current.MessageId, session.TmpContents[0].MsgID)
}

func TestNewSessionRoutingTakesPrecedence(t *testing.T) {
	createNew, ignoreTimeout, sessionID := parseSessionRouting("continue @last @123 @new")
	require.True(t, createNew)
	require.False(t, ignoreTimeout)
	require.Zero(t, sessionID)

	createNew, ignoreTimeout, sessionID = parseSessionRouting("continue @last @123")
	require.False(t, createNew)
	require.True(t, ignoreTimeout)
	require.Zero(t, sessionID)

	createNew, ignoreTimeout, sessionID = parseSessionRouting("continue @123")
	require.False(t, createNew)
	require.False(t, ignoreTimeout)
	require.Equal(t, int64(123), sessionID)

	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })
	require.True(t, IsGeminiReq(&gotgbot.Message{
		Text: "@new", From: &gotgbot.User{Id: 42},
		ReplyToMessage: &gotgbot.Message{From: &gotgbot.User{Id: 7}},
	}))
}

func TestNewSessionReplyIsContextOnlyAndKeepsOriginalSessionOwnership(t *testing.T) {
	ctx := context.Background()
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })

	const chatID int64 = -910113
	oldSession, err := g.Q.CreateNewGeminiSession(ctx, chatID, "old", "supergroup")
	require.NoError(t, err)
	oldReply := q.GeminiContent{SessionID: oldSession.ID, ChatID: chatID, MsgID: 892001,
		Role: genai.RoleUser, SentTime: q.UnixTime{Time: time.Unix(120, 0)}, Username: "Bob",
		MsgType: "text", Text: sql.NullString{String: "old session message", Valid: true}, UserID: 7}
	require.NoError(t, oldReply.Save(ctx, g.Q))

	newSession, err := g.Q.CreateNewGeminiSession(ctx, chatID, "new", "supergroup")
	require.NoError(t, err)
	replied := &gotgbot.Message{MessageId: oldReply.MsgID, Date: 120,
		Chat: gotgbot.Chat{Id: chatID, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, FirstName: "Bob"}, Text: "old session message"}
	current := &gotgbot.Message{MessageId: 892002, Date: 123,
		Chat: gotgbot.Chat{Id: chatID, Type: "supergroup"},
		From: &gotgbot.User{Id: 42, FirstName: "Alice"}, Text: "@new continue", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: newSession, Provider: ProviderGemini}
	require.NoError(t, session.AddTgMessageWithReplyMode(ctx, nil, current, true))
	require.Len(t, session.TmpContents, 2, "the replied message is still sent as new-session context")
	require.Contains(t, session.TmpContextOnlyMsgIDs, oldReply.MsgID)
	require.NoError(t, session.PersistTmpUpdates(ctx))

	replySessionID, err := g.Q.GetSessionIdByMessage(ctx, chatID, oldReply.MsgID)
	require.NoError(t, err)
	require.Equal(t, oldSession.ID, replySessionID)
	currentSessionID, err := g.Q.GetSessionIdByMessage(ctx, chatID, current.MessageId)
	require.NoError(t, err)
	require.Equal(t, newSession.ID, currentSessionID)
}

func TestNewSessionReplyWithoutSessionRemainsUnassigned(t *testing.T) {
	ctx := context.Background()
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })

	const chatID int64 = -910114
	newSession, err := g.Q.CreateNewGeminiSession(ctx, chatID, "new", "supergroup")
	require.NoError(t, err)
	replied := &gotgbot.Message{MessageId: 893001, Date: 120,
		Chat: gotgbot.Chat{Id: chatID, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, FirstName: "Bob"}, Text: "unassigned message"}
	current := &gotgbot.Message{MessageId: 893002, Date: 123,
		Chat: gotgbot.Chat{Id: chatID, Type: "supergroup"},
		From: &gotgbot.User{Id: 42, FirstName: "Alice"}, Text: "@new continue", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: newSession, Provider: ProviderGemini}
	require.NoError(t, session.AddTgMessageWithReplyMode(ctx, nil, current, true))
	require.Len(t, session.TmpContents, 2)
	require.Contains(t, session.TmpContextOnlyMsgIDs, replied.MessageId)
	require.NoError(t, session.PersistTmpUpdates(ctx))

	_, err = g.Q.GetSessionIdByMessage(ctx, chatID, replied.MessageId)
	require.ErrorIs(t, err, sql.ErrNoRows)
	currentSessionID, err := g.Q.GetSessionIdByMessage(ctx, chatID, current.MessageId)
	require.NoError(t, err)
	require.Equal(t, newSession.ID, currentSessionID)
}

func TestReplyOutsideActiveWindowIsContextOnlyAndDoesNotDuplicateDatabaseRow(t *testing.T) {
	ctx := context.Background()
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	oldSession, err := g.Q.CreateNewGeminiSession(ctx, -910103, "old", "supergroup")
	require.NoError(t, err)
	oldContent := q.GeminiContent{SessionID: oldSession.ID, ChatID: -910103, MsgID: 882001,
		Role: genai.RoleUser, SentTime: q.UnixTime{Time: time.Unix(120, 0)}, Username: "Bob",
		MsgType: "text", Text: sql.NullString{String: "stored old reply", Valid: true}, UserID: 7}
	require.NoError(t, oldContent.Save(ctx, g.Q))
	currentSession, err := g.Q.CreateNewGeminiSession(ctx, -910103, "current", "supergroup")
	require.NoError(t, err)
	replied := &gotgbot.Message{MessageId: 882001, Date: 120,
		Chat: gotgbot.Chat{Id: -910103, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, FirstName: "Bob"}, Text: "stored old reply"}
	current := &gotgbot.Message{MessageId: 882002, Date: 123,
		Chat: gotgbot.Chat{Id: -910103, Type: "supergroup"},
		From: &gotgbot.User{Id: 42, FirstName: "Alice"}, Text: "new", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: currentSession}
	require.NoError(t, session.AddTgMessageWithReply(ctx, nil, current))
	require.Contains(t, session.TmpContextOnlyMsgIDs, int64(882001))
	require.NoError(t, session.PersistTmpUpdates(ctx))
	saved, err := g.Q.GetAllMsgInSession(ctx, currentSession.ID, 10)
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.Equal(t, int64(882002), saved[0].MsgID)
	require.Len(t, session.Contents, 2, "the replied message remains in the active in-memory chain")
}

type localMediaBotClient struct {
	paths map[string]string
}

func (c localMediaBotClient) RequestWithContext(_ context.Context, _ string, method string,
	params map[string]any, _ *gotgbot.RequestOpts,
) (json.RawMessage, error) {
	if method != "getFile" {
		return nil, fmt.Errorf("unexpected bot method %s", method)
	}
	fileID, _ := params["file_id"].(string)
	return json.Marshal(gotgbot.File{FileId: fileID, FileUniqueId: fileID + "-unique", FilePath: c.paths[fileID]})
}

func (localMediaBotClient) GetAPIURL(*gotgbot.RequestOpts) string { return "https://example.test" }
func (localMediaBotClient) FileURL(string, string, *gotgbot.RequestOpts) string {
	return "https://example.test/file"
}

func TestReplyPhotoAndVideoUseNormalGeminiMediaParts(t *testing.T) {
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	photoPath := t.TempDir() + "/photo.jpg"
	videoPath := t.TempDir() + "/video.mp4"
	require.NoError(t, os.WriteFile(photoPath, []byte("photo-bytes"), 0o600))
	require.NoError(t, os.WriteFile(videoPath, []byte("video-bytes"), 0o600))
	bot := &gotgbot.Bot{BotClient: localMediaBotClient{paths: map[string]string{
		"reply-photo-file": photoPath, "reply-video-file": videoPath,
	}}}
	user := &gotgbot.User{Id: 42, FirstName: "Alice"}

	photoReply := &gotgbot.Message{MessageId: 883001, Date: 120,
		Chat: gotgbot.Chat{Id: -910104, Type: "supergroup"}, From: user,
		Photo: []gotgbot.PhotoSize{{FileId: "reply-photo-file", FileUniqueId: "photo-unique"}}}
	photoCurrent := &gotgbot.Message{MessageId: 883002, Date: 123,
		Chat: gotgbot.Chat{Id: -910104, Type: "supergroup"}, From: user,
		Text: "describe it", ReplyToMessage: photoReply}
	photoSession := &GeminiSession{GeminiSession: q.GeminiSession{ID: 810104}, Provider: ProviderGemini}
	require.NoError(t, photoSession.AddTgMessageWithReply(context.Background(), bot, photoCurrent))
	require.Len(t, photoSession.TmpContents, 2)
	require.Equal(t, "photo", photoSession.TmpContents[0].MsgType)
	require.Equal(t, []byte("photo-bytes"), photoSession.TmpContents[0].Blob)
	photoSteps, err := interactionStepsForContents(photoSession.TmpContents, nil)
	require.NoError(t, err)
	require.Contains(t, string(bytesJoinRaw(photoSteps)), `"type":"image"`)

	videoReply := &gotgbot.Message{MessageId: 884001, Date: 120,
		Chat: gotgbot.Chat{Id: -910105, Type: "supergroup"}, From: user,
		Video: &gotgbot.Video{FileId: "reply-video-file", FileUniqueId: "video-unique",
			Duration: 10, FileSize: int64(len("video-bytes"))}}
	videoCurrent := &gotgbot.Message{MessageId: 884002, Date: 123,
		Chat: gotgbot.Chat{Id: -910105, Type: "supergroup"}, From: user,
		Text: "summarize it", ReplyToMessage: videoReply}
	videoSession := &GeminiSession{GeminiSession: q.GeminiSession{ID: 810105}, Provider: ProviderGemini}
	require.NoError(t, videoSession.AddTgMessageWithReply(context.Background(), bot, videoCurrent))
	require.Len(t, videoSession.TmpContents, 2)
	require.Equal(t, "video", videoSession.TmpContents[0].MsgType)
	require.Equal(t, []byte("video-bytes"), videoSession.TmpContents[0].Blob)
	videoSteps, err := interactionStepsForContents(videoSession.TmpContents, nil)
	require.NoError(t, err)
	require.Contains(t, string(bytesJoinRaw(videoSteps)), `"type":"video"`)
}

func TestBotRepliesWithoutAIResponseMetadataBecomeUserInputAndKeepMedia(t *testing.T) {
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	photoPath := t.TempDir() + "/bot-photo.jpg"
	videoPath := t.TempDir() + "/bot-video.mp4"
	require.NoError(t, os.WriteFile(photoPath, []byte("bot-photo-bytes"), 0o600))
	require.NoError(t, os.WriteFile(videoPath, []byte("bot-video-bytes"), 0o600))
	bot := &gotgbot.Bot{BotClient: localMediaBotClient{paths: map[string]string{
		"bot-reply-photo": photoPath, "bot-reply-video": videoPath,
	}}}
	botSender := &gotgbot.User{Id: mainBot.Id, FirstName: "bot"}
	requester := &gotgbot.User{Id: 42, FirstName: "Alice"}
	tests := []struct {
		name      string
		chatID    int64
		replied   *gotgbot.Message
		wantBlock string
	}{
		{name: "text", chatID: -910107, wantBlock: `"type":"user_input"`,
			replied: &gotgbot.Message{MessageId: 886001, Date: 120, From: botSender, Text: "non-AI notice"}},
		{name: "photo", chatID: -910108, wantBlock: `"type":"image"`,
			replied: &gotgbot.Message{MessageId: 887001, Date: 120, From: botSender,
				Photo: []gotgbot.PhotoSize{{FileId: "bot-reply-photo", FileUniqueId: "bot-photo-unique"}}}},
		{name: "video", chatID: -910109, wantBlock: `"type":"video"`,
			replied: &gotgbot.Message{MessageId: 888001, Date: 120, From: botSender,
				Video: &gotgbot.Video{FileId: "bot-reply-video", FileUniqueId: "bot-video-unique",
					Duration: 10, FileSize: int64(len("bot-video-bytes"))}}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.replied.Chat = gotgbot.Chat{Id: test.chatID, Type: "supergroup"}
			current := &gotgbot.Message{MessageId: test.replied.MessageId + 1, Date: 123,
				Chat: gotgbot.Chat{Id: test.chatID, Type: "supergroup"}, From: requester,
				Text: "use the replied message", ReplyToMessage: test.replied}
			session := &GeminiSession{GeminiSession: q.GeminiSession{ID: int64(820000 + index)},
				Provider: ProviderGemini}
			require.NoError(t, session.AddTgMessageWithReply(context.Background(), bot, current))
			require.Len(t, session.TmpContents, 2)
			require.Equal(t, genai.RoleUser, session.TmpContents[0].Role,
				"an unrecorded Bot message must not masquerade as AI assistant history")
			steps, err := interactionStepsForContents(session.TmpContents, nil)
			require.NoError(t, err)
			joined := string(bytesJoinRaw(steps))
			require.Contains(t, joined, `"type":"user_input"`)
			require.Contains(t, joined, test.wantBlock)
		})
	}

	ctx := context.Background()
	storedSession, err := g.Q.CreateNewGeminiSession(ctx, -910110, "stored non-AI bot message", "supergroup")
	require.NoError(t, err)
	storedNonAI := q.GeminiContent{SessionID: storedSession.ID, ChatID: -910110, MsgID: 889001,
		Role: genai.RoleModel, SentTime: q.UnixTime{Time: time.Unix(120, 0)}, Username: "bot",
		MsgType: "text", Text: sql.NullString{String: "legacy non-AI notice", Valid: true}, UserID: mainBot.Id}
	require.NoError(t, storedNonAI.Save(ctx, g.Q))
	storedReply := &gotgbot.Message{MessageId: 889001, Date: 120,
		Chat: gotgbot.Chat{Id: -910110, Type: "supergroup"}, From: botSender, Text: "legacy non-AI notice"}
	storedCurrent := &gotgbot.Message{MessageId: 889002, Date: 123,
		Chat: gotgbot.Chat{Id: -910110, Type: "supergroup"}, From: requester,
		Text: "use this too", ReplyToMessage: storedReply}
	target := &GeminiSession{GeminiSession: q.GeminiSession{ID: 820110}, Provider: ProviderGemini}
	require.NoError(t, target.AddTgMessageWithReply(ctx, bot, storedCurrent))
	require.Equal(t, genai.RoleUser, target.TmpContents[0].Role,
		"database presence alone must not classify a Bot message as an AI response")
	require.Contains(t, target.TmpContextOnlyMsgIDs, int64(889001))

	activeSession, err := g.Q.CreateNewGeminiSession(ctx, -910111, "active legacy non-AI bot message", "supergroup")
	require.NoError(t, err)
	activeNonAI := q.GeminiContent{SessionID: activeSession.ID, ChatID: -910111, MsgID: 890001,
		Role: genai.RoleModel, SentTime: q.UnixTime{Time: time.Unix(120, 0)}, Username: "bot",
		MsgType: "text", Text: sql.NullString{String: "active legacy notice", Valid: true}, UserID: mainBot.Id}
	require.NoError(t, activeNonAI.Save(ctx, g.Q))
	activeReply := &gotgbot.Message{MessageId: 890001, Date: 120,
		Chat: gotgbot.Chat{Id: -910111, Type: "supergroup"}, From: botSender, Text: "active legacy notice"}
	activeCurrent := &gotgbot.Message{MessageId: 890002, Date: 123,
		Chat: gotgbot.Chat{Id: -910111, Type: "supergroup"}, From: requester,
		Text: "use the active notice", ReplyToMessage: activeReply}
	activeTarget := &GeminiSession{GeminiSession: activeSession,
		Provider: ProviderGemini, Contents: []q.GeminiContent{activeNonAI}}
	require.NoError(t, activeTarget.AddTgMessageWithReply(ctx, bot, activeCurrent))
	require.Equal(t, genai.RoleUser, activeTarget.Contents[0].Role)
	require.Len(t, activeTarget.TmpContents, 1, "an active reply must not be appended twice")
	reloaded, err := g.Q.GetAllMsgInSession(ctx, activeSession.ID, 10)
	require.NoError(t, err)
	require.Equal(t, genai.RoleUser, reloaded[0].Role,
		"the on-use repair must survive a service restart")

	aiSession, err := g.Q.CreateNewGeminiSession(ctx, -910112, "real AI response", "supergroup")
	require.NoError(t, err)
	aiContent := q.GeminiContent{SessionID: aiSession.ID, ChatID: -910112, MsgID: 891001,
		Role: genai.RoleModel, SentTime: q.UnixTime{Time: time.Unix(120, 0)}, Username: "bot",
		MsgType: "text", Text: sql.NullString{String: "real AI answer", Valid: true}, UserID: mainBot.Id}
	require.NoError(t, aiContent.Save(ctx, g.Q))
	require.NoError(t, g.SetAIMessageUsage(ctx, aiSession.ID, 891001, -910112,
		ProviderGemini, ModelGeminiFlash, 10, 2, 0))
	aiReply := &gotgbot.Message{MessageId: 891001, Date: 120,
		Chat: gotgbot.Chat{Id: -910112, Type: "supergroup"}, From: botSender, Text: "real AI answer"}
	aiCurrent := &gotgbot.Message{MessageId: 891002, Date: 123,
		Chat: gotgbot.Chat{Id: -910112, Type: "supergroup"}, From: requester,
		Text: "continue", ReplyToMessage: aiReply}
	aiTarget := &GeminiSession{GeminiSession: aiSession,
		Provider: ProviderGemini, Contents: []q.GeminiContent{aiContent}}
	require.NoError(t, aiTarget.AddTgMessageWithReply(ctx, bot, aiCurrent))
	require.Equal(t, genai.RoleModel, aiTarget.Contents[0].Role,
		"a recorded AI response must remain assistant history")
}

func TestCaptionlessDeepSeekReplyVideoIsSkippedWithoutPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	dbSession, err := g.Q.CreateNewGeminiSession(ctx, -910106, "deepseek reply video", "supergroup")
	require.NoError(t, err)
	user := &gotgbot.User{Id: 42, FirstName: "Alice"}
	replied := &gotgbot.Message{MessageId: 885001, Date: 120,
		Chat: gotgbot.Chat{Id: -910106, Type: "supergroup"}, From: user,
		Video: &gotgbot.Video{FileId: "unused", FileUniqueId: "unused", Duration: 10, FileSize: 10}}
	current := &gotgbot.Message{MessageId: 885002, Date: 123,
		Chat: gotgbot.Chat{Id: -910106, Type: "supergroup"}, From: user,
		Text: "continue", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: dbSession, Provider: ProviderDeepSeek, Model: ModelDeepSeekFlash}
	require.NoError(t, session.AddTgMessageWithReply(ctx, nil, current))
	require.Contains(t, session.TmpContextOnlyMsgIDs, int64(885001))
	messages, err := session.ToDeepSeekMessages("system")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Contains(t, messages[1].Content, "continue")
	require.NoError(t, session.PersistTmpUpdates(ctx))
	saved, err := g.Q.GetAllMsgInSession(ctx, dbSession.ID, 10)
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.Equal(t, int64(885002), saved[0].MsgID)
}

func TestPersistTmpUpdatesStoresBodyPayloadAndUsageAtomically(t *testing.T) {
	ctx := context.Background()
	dbSession, err := g.Q.CreateNewGeminiSession(ctx, -900001, "transaction test", "private")
	require.NoError(t, err)
	require.NoError(t, g.SetAISessionModel(ctx, dbSession.ID, ProviderDeepSeek, ModelDeepSeekFlash))
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })

	userContent := testContent("text", "hello")
	userContent.SessionID = dbSession.ID
	userContent.ChatID = -900001
	userContent.MsgID = 5001
	session := &GeminiSession{
		GeminiSession: dbSession,
		Provider:      ProviderDeepSeek,
		Model:         ModelDeepSeekFlash,
		TmpContents:   []q.GeminiContent{userContent},
	}
	result := &AIResult{
		DisplayText:            "answer",
		AssistantPayload:       []byte(`{"role":"assistant","reasoning_content":"think","content":"answer"}`),
		AssistantPayloadFormat: PayloadFormatDeepSeekMessage,
		Usage:                  AIUsage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 80},
		InteractionID:          "int-persisted",
		WindowStartMsgID:       5001,
		InputMessageCount:      1,
		InputFirstMsgID:        5001,
		InputLastMsgID:         5001,
	}
	session.AddModelMessage(&gotgbot.Message{
		MessageId: 5002, Date: 123, Chat: gotgbot.Chat{Id: -900001, Type: "private"},
	}, result)
	require.NoError(t, session.PersistTmpUpdates(ctx))
	require.Empty(t, session.TmpContents)
	require.Empty(t, session.PendingResponses)

	savedContents, err := g.Q.GetAllMsgInSession(ctx, dbSession.ID, 10)
	require.NoError(t, err)
	require.Len(t, savedContents, 2)
	require.Equal(t, "answer", savedContents[1].Text.String)
	payloads, err := g.GetAISessionAssistantPayloads(ctx, dbSession.ID)
	require.NoError(t, err)
	require.Equal(t, result.AssistantPayload, payloads[5002].Payload)
	usage, err := g.GetAIMessageUsage(ctx, -900001, 5002)
	require.NoError(t, err)
	require.Equal(t, int64(80), usage.CachedInputTokens)
	require.Equal(t, dbSession.ID, usage.SessionID)
	require.Equal(t, int64(1), usage.InputMessageCount)
	require.Equal(t, int64(5001), usage.InputFirstMsgID)
	require.Equal(t, int64(5001), usage.InputLastMsgID)
	runtimeState, err := g.GetAISessionRuntimeState(ctx, dbSession.ID)
	require.NoError(t, err)
	require.Equal(t, "int-persisted", runtimeState.GeminiInteractionID)
	require.Equal(t, int64(5001), runtimeState.WindowStartMsgID)

	failedDBSession, err := g.Q.CreateNewGeminiSession(ctx, -900002, "rollback test", "private")
	require.NoError(t, err)
	duplicate := testContent("text", "duplicate")
	duplicate.SessionID = failedDBSession.ID
	duplicate.ChatID = -900002
	duplicate.MsgID = 6001
	failed := &GeminiSession{
		GeminiSession: failedDBSession,
		Provider:      ProviderDeepSeek,
		Model:         ModelDeepSeekFlash,
		TmpContents:   []q.GeminiContent{duplicate, duplicate},
	}
	failed.AddModelMessage(&gotgbot.Message{
		MessageId: 6002, Date: 123, Chat: gotgbot.Chat{Id: -900002, Type: "private"},
	}, result)
	require.Error(t, failed.PersistTmpUpdates(ctx))
	rolledBack, err := g.Q.GetAllMsgInSession(ctx, failedDBSession.ID, 10)
	require.NoError(t, err)
	require.Empty(t, rolledBack)
	_, err = g.GetAIMessageUsage(ctx, -900002, 6002)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestExplicitCacheStatePersistsWithResponseAndReloads(t *testing.T) {
	ctx := context.Background()
	dbSession, err := g.Q.CreateNewGeminiSession(ctx, -900020, "explicit cache state", "private")
	require.NoError(t, err)
	require.NoError(t, g.SetAISessionModel(ctx, dbSession.ID, ProviderGemini, ModelGeminiFlash))
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })

	userContent := testContent("text", "hello")
	userContent.SessionID = dbSession.ID
	userContent.ChatID = -900020
	userContent.MsgID = 6101
	expireTime := time.Unix(1_800_000_000, 0)
	session := &GeminiSession{
		GeminiSession: dbSession, Provider: ProviderGemini, Model: ModelGeminiFlash,
		TmpContents: []q.GeminiContent{userContent},
		GeminiCache: geminiExplicitCacheState{
			Name: "cachedContents/session-cache", ExpireTime: expireTime,
			StartMsgID: 6101, EndMsgID: 6101, TokenCount: 5000, Fingerprint: "fingerprint",
		},
	}
	session.AddModelMessage(&gotgbot.Message{
		MessageId: 6102, Date: 123, Chat: gotgbot.Chat{Id: -900020, Type: "private"},
	}, &AIResult{
		DisplayText: "answer", AssistantPayload: []byte(`{"role":"model","parts":[{"text":"answer"}]}`),
		AssistantPayloadFormat: PayloadFormatGeminiContent, WindowStartMsgID: 6101,
	})
	require.NoError(t, session.PersistTmpUpdates(ctx))

	runtimeState, err := g.GetAISessionRuntimeState(ctx, dbSession.ID)
	require.NoError(t, err)
	require.Equal(t, "cachedContents/session-cache", runtimeState.GeminiCacheName)
	require.Equal(t, expireTime.Unix(), runtimeState.GeminiCacheExpireTime)
	require.Equal(t, int64(6101), runtimeState.GeminiCacheStartMsgID)
	require.Equal(t, int64(6101), runtimeState.GeminiCacheEndMsgID)
	require.Equal(t, int64(5000), runtimeState.GeminiCacheTokenCount)
	require.Equal(t, "fingerprint", runtimeState.GeminiCacheFingerprint)

	reloaded := &GeminiSession{GeminiSession: dbSession}
	require.NoError(t, reloaded.loadContentFromDatabase(ctx))
	require.NoError(t, reloaded.loadModel(ctx, defaultAIModel))
	require.Equal(t, session.GeminiCache, reloaded.GeminiCache)
}

func TestPersistedSlidingWindowKeepsFullDatabaseHistory(t *testing.T) {
	ctx := context.Background()
	dbSession, err := g.Q.CreateNewGeminiSession(ctx, -900003, "window persistence", "private")
	require.NoError(t, err)
	require.NoError(t, g.SetAISessionModel(ctx, dbSession.ID, ProviderGemini, ModelGeminiFlash))

	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })

	session := &GeminiSession{
		GeminiSession: dbSession, Provider: ProviderGemini, Model: ModelGeminiFlash,
		AssistantPayloads: make(map[int64]g.AIAssistantPayload),
	}
	for id := int64(1); id <= 200; id++ {
		content := interactionTestContent(id+7000, genai.RoleUser, "history")
		content.SessionID = dbSession.ID
		content.ChatID = -900003
		session.TmpContents = append(session.TmpContents, content)
	}
	require.NoError(t, session.PersistTmpUpdates(ctx))
	require.Len(t, session.Contents, 200)

	current := interactionTestContent(7201, genai.RoleUser, "current")
	current.SessionID = dbSession.ID
	current.ChatID = -900003
	session.TmpContents = []q.GeminiContent{current}
	window := session.prepareRequestWindow()
	require.Equal(t, 50, window.Drop)
	result := &AIResult{
		DisplayText: "answer", AssistantPayload: []byte(`[{"type":"model_output","content":[{"type":"text","text":"answer"}]}]`),
		AssistantPayloadFormat: PayloadFormatGeminiInteractionSteps,
		InteractionID:          "int-window", WindowStartMsgID: window.StartMsgID, WindowDrop: window.Drop,
	}
	session.AddModelMessage(&gotgbot.Message{
		MessageId: 7202, Date: 123, Chat: gotgbot.Chat{Id: -900003, Type: "private"},
	}, result)
	require.NoError(t, session.PersistTmpUpdates(ctx))
	require.Len(t, session.Contents, 152)
	require.Equal(t, int64(7051), session.Contents[0].MsgID)

	allDatabaseContents, err := g.Q.GetAllMsgInSession(ctx, dbSession.ID, 500)
	require.NoError(t, err)
	require.Len(t, allDatabaseContents, 202)

	reloaded := &GeminiSession{GeminiSession: dbSession}
	require.NoError(t, reloaded.loadContentFromDatabase(ctx))
	require.NoError(t, reloaded.loadModel(ctx, defaultAIModel))
	require.Len(t, reloaded.Contents, 152)
	require.Equal(t, int64(7051), reloaded.Contents[0].MsgID)
	require.Equal(t, "int-window", reloaded.GeminiInteractionID)
}

func ptr[T any](value T) *T { return &value }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestModelKeyboardMarksCurrent(t *testing.T) {
	keyboard := modelKeyboard(ModelDeepSeekFlash, 12345)
	require.Len(t, keyboard.InlineKeyboard, 3)
	require.True(t, strings.HasPrefix(keyboard.InlineKeyboard[2][0].Text, "✅"))
	for _, row := range keyboard.InlineKeyboard {
		require.LessOrEqual(t, len(row[0].CallbackData), 64)
		sessionID, model, ok := parseModelCallback(row[0].CallbackData)
		require.True(t, ok)
		require.Equal(t, int64(12345), sessionID)
		require.NotEmpty(t, model)
	}
	legacySessionID, legacyModel, ok := parseModelCallback(modelCallbackPrefix + ModelGeminiFlash)
	require.True(t, ok)
	require.Zero(t, legacySessionID)
	require.Equal(t, ModelGeminiFlash, legacyModel)
}

func TestGemini3FlashDefaultsToLowThinkingWithoutOverridingExplicitLevel(t *testing.T) {
	flashConfig := &genai.GenerateContentConfig{}
	configureGeminiThinking(ModelGeminiFlash, flashConfig)
	require.True(t, flashConfig.ThinkingConfig.IncludeThoughts)
	require.Equal(t, genai.ThinkingLevelLow, flashConfig.ThinkingConfig.ThinkingLevel)

	liteConfig := &genai.GenerateContentConfig{}
	configureGeminiThinking(ModelGeminiFlashLite, liteConfig)
	require.True(t, liteConfig.ThinkingConfig.IncludeThoughts)
	require.Empty(t, liteConfig.ThinkingConfig.ThinkingLevel)

	explicitConfig := &genai.GenerateContentConfig{ThinkingConfig: &genai.ThinkingConfig{
		ThinkingLevel: genai.ThinkingLevelHigh,
	}}
	configureGeminiThinking(ModelGeminiFlash, explicitConfig)
	require.Equal(t, genai.ThinkingLevelHigh, explicitConfig.ThinkingConfig.ThinkingLevel)
}

func TestPrivateChatCanChangeModel(t *testing.T) {
	ok, err := canChangeModel(&gotgbot.Bot{}, &gotgbot.Chat{Type: "private"}, 42)
	require.NoError(t, err)
	require.True(t, ok)
}

type fixedBotClient struct {
	status string
}

func (c fixedBotClient) RequestWithContext(_ context.Context, _ string, method string,
	_ map[string]any, _ *gotgbot.RequestOpts,
) (json.RawMessage, error) {
	if method != "getChatMember" {
		return nil, nil
	}
	return json.RawMessage(`{"status":"` + c.status + `","user":{"id":42,"is_bot":false,"first_name":"user"}}`), nil
}

func (fixedBotClient) GetAPIURL(*gotgbot.RequestOpts) string { return "https://example.test" }
func (fixedBotClient) FileURL(string, string, *gotgbot.RequestOpts) string {
	return "https://example.test/file"
}

func TestGroupAIConfigPermission(t *testing.T) {
	chat := &gotgbot.Chat{Id: -1001, Type: "supergroup"}
	adminBot := &gotgbot.Bot{BotClient: fixedBotClient{status: "administrator"}}
	allowed, err := canChangeModel(adminBot, chat, 42)
	require.NoError(t, err)
	require.True(t, allowed)

	memberBot := &gotgbot.Bot{BotClient: fixedBotClient{status: "member"}}
	allowed, err = canChangeModel(memberBot, chat, 42)
	require.NoError(t, err)
	require.False(t, allowed)

	allowed, err = canChangeModel(&gotgbot.Bot{}, chat, g.GetConfig().God)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestUsageButtonAndAlertFormatting(t *testing.T) {
	keyboard := usageKeyboard()
	require.Equal(t, usageCallbackData, keyboard.InlineKeyboard[0][0].CallbackData)
	require.True(t, IsUsageCallback(&gotgbot.CallbackQuery{Data: usageCallbackData}))
	require.False(t, IsUsageCallback(&gotgbot.CallbackQuery{Data: usageCallbackData + ":forged"}))

	alert := formatUsageAlert(g.AIMessageUsage{
		SessionID: 7763, Model: ModelDeepSeekFlash, InputTokens: 27014, OutputTokens: 32,
		CachedInputTokens: 27008, InputMessageCount: 174, InputFirstMsgID: 100, InputLastMsgID: 274,
	})
	require.Equal(t, "ID: 7763\n消息: 174（100-274）\n模型: deepseek-v4-flash\n输入: 27.01 ktoken\n输出: 0.03 ktoken\n缓存: 27.01 ktoken\n缓存到期：无显式缓存", alert)
	require.LessOrEqual(t, len([]rune(alert)), 200)

	cacheExpire := time.Date(2026, 7, 15, 3, 22, 33, 0, time.UTC).Unix()
	alert = formatUsageAlert(g.AIMessageUsage{
		SessionID: 7763, Model: ModelGeminiFlash, GeminiCacheExpireTime: cacheExpire,
	})
	require.Contains(t, alert, "缓存到期：2026-07-15 11:22:33")

	longAlert := formatUsageAlert(g.AIMessageUsage{Model: strings.Repeat("模", 300)})
	require.Len(t, []rune(longAlert), 200)
}

type captureBotClient struct {
	method string
	params map[string]any
}

func (c *captureBotClient) RequestWithContext(_ context.Context, _ string, method string,
	params map[string]any, _ *gotgbot.RequestOpts,
) (json.RawMessage, error) {
	c.method = method
	c.params = params
	return json.RawMessage(`true`), nil
}

func (*captureBotClient) GetAPIURL(*gotgbot.RequestOpts) string { return "https://example.test" }
func (*captureBotClient) FileURL(string, string, *gotgbot.RequestOpts) string {
	return "https://example.test/file"
}

func usageCallbackContext(userID int64) *ext.Context {
	query := &gotgbot.CallbackQuery{Id: "callback", Data: usageCallbackData,
		From: gotgbot.User{Id: userID, FirstName: "viewer"}}
	return &ext.Context{
		Update:           &gotgbot.Update{CallbackQuery: query},
		EffectiveMessage: &gotgbot.Message{MessageId: 99, Chat: gotgbot.Chat{Id: -1001, Type: "supergroup"}},
		EffectiveUser:    &query.From,
	}
}

func TestUsageCallbackAllowsAnyViewerAndUsesMessageIdentity(t *testing.T) {
	client := &captureBotClient{}
	bot := &gotgbot.Bot{BotClient: client}
	ctx := usageCallbackContext(987654321)
	err := showUsageByButton(bot, ctx, func(_ context.Context, chatID, msgID int64) (g.AIMessageUsage, error) {
		require.Equal(t, int64(-1001), chatID)
		require.Equal(t, int64(99), msgID)
		return g.AIMessageUsage{Model: ModelGeminiFlash, InputTokens: 1000, OutputTokens: 200, CachedInputTokens: 800}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "answerCallbackQuery", client.method)
	require.Equal(t, true, client.params["show_alert"])
	require.Contains(t, client.params["text"], "缓存: 0.80 ktoken")
}

func TestUsageCallbackMissingRecord(t *testing.T) {
	client := &captureBotClient{}
	err := showUsageByButton(&gotgbot.Bot{BotClient: client}, usageCallbackContext(42),
		func(context.Context, int64, int64) (g.AIMessageUsage, error) {
			return g.AIMessageUsage{}, sql.ErrNoRows
		})
	require.NoError(t, err)
	require.Equal(t, true, client.params["show_alert"])
	require.Contains(t, client.params["text"], "不存在")
}
