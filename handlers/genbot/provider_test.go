package genbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	g "main/globalcfg"
	"main/globalcfg/aiq"
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

}

func createV2TestSession(t *testing.T, chatID int64, provider, model string) q.GeminiSession {
	t.Helper()
	now := time.Now().Unix()
	row, err := g.AIQ.CreateAISession(context.Background(), aiq.CreateAISessionParams{
		ChatID: chatID, ChatName: "test", ChatType: "private", Provider: provider, Model: model,
		CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	return sessionFromV2(row)
}

func TestGeminiGetSessionRejectsCrossChatExplicitSessionID(t *testing.T) {
	tests := []struct {
		name   string
		cached bool
	}{
		{name: "database"},
		{name: "cache", cached: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			foreignChatID := int64(-930100 - index*2)
			currentChatID := foreignChatID - 1
			foreign := createV2TestSession(t, foreignChatID, ProviderGemini, ModelGeminiFlash)
			if test.cached {
				geminiSessions.mu.Lock()
				geminiSessions.sidToSess[foreign.ID] = &GeminiSession{
					GeminiSession: foreign,
					Provider:      ProviderGemini,
					Model:         ModelGeminiFlash,
				}
				geminiSessions.mu.Unlock()
			}
			t.Cleanup(func() { invalidateSession(foreign.ID) })

			msg := &gotgbot.Message{
				MessageId: int64(930100 + index),
				Chat: gotgbot.Chat{
					Id: currentChatID, Type: "private", FirstName: "requester",
				},
			}
			session := GeminiGetSession(context.Background(), msg, false, false, foreign.ID)
			require.NotNil(t, session)
			t.Cleanup(func() { invalidateSession(session.ID) })
			require.NotEqual(t, foreign.ID, session.ID)
			require.Equal(t, currentChatID, session.ChatID)
		})
	}
}

func v2TestUserContent(sessionID, chatID, msgID int64, text string) q.GeminiContent {
	content := testContent("text", text)
	content.SessionID = sessionID
	content.ChatID = chatID
	content.MsgID = msgID
	content.UserID = 42
	return content
}

func TestAIRunLifecyclePersistsRetryStateAndCountsTokensOnce(t *testing.T) {
	ctx := context.Background()
	const chatID int64 = -920001
	stored := createV2TestSession(t, chatID, ProviderGemini, ModelGeminiFlash)
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })

	session := &GeminiSession{
		GeminiSession: stored, Provider: ProviderGemini, Model: ModelGeminiFlash,
		TmpContents: []q.GeminiContent{v2TestUserContent(stored.ID, chatID, 5001, "hello")},
		GeminiCache: geminiExplicitCacheState{
			Name: "cachedContents/run", ExpireTime: time.Unix(1_800_000_000, 0),
			StartMsgID: 5001, EndMsgID: 5001, TokenCount: 5000, Fingerprint: "fingerprint",
		},
	}
	run, err := session.BeginAIRun(ctx)
	require.NoError(t, err)
	require.Equal(t, "pending", run.Status)
	retry, err := session.GetOrBeginAIRun(ctx, chatID, 5001)
	require.NoError(t, err)
	require.Equal(t, run.ID, retry.ID)

	result := &AIResult{
		DisplayText: "answer", Raw: []byte(`{"provider":"raw"}`),
		AssistantPayload:       []byte(`{"role":"model","parts":[{"text":"answer"}]}`),
		AssistantPayloadFormat: PayloadFormatGeminiContent,
		Usage:                  AIUsage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 80},
		InteractionID:          "int-persisted", WindowStartMsgID: 5001,
		InputMessageCount: 1, InputFirstMsgID: 5001, InputLastMsgID: 5001,
	}
	require.NoError(t, session.CompleteAIRun(ctx, run.ID, result))
	require.NoError(t, session.CompleteAIRun(ctx, run.ID, result), "a repeated completion must be idempotent")
	savedSession, err := g.AIQ.GetAISession(ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), savedSession.TotalInputTokens)
	require.Equal(t, int64(20), savedSession.TotalOutputTokens)
	require.Equal(t, int64(80), savedSession.TotalCachedInputTokens)
	_, err = g.AIQ.GetAISessionProviderState(ctx, stored.ID)
	require.ErrorIs(t, err, sql.ErrNoRows, "candidate provider state must not be promoted before delivery")

	require.NoError(t, MarkAIRunFailed(ctx, run.ID, "delivery_failed", "telegram", errors.New("send failed")))
	savedRun, err := g.AIQ.GetAIRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "delivery_failed", savedRun.Status)
	require.Equal(t, result.Raw, savedRun.RawPayload)
	restoredResult := resultFromStoredRun(savedRun)
	require.Equal(t, result.DisplayText, restoredResult.DisplayText)

	response := &gotgbot.Message{MessageId: 5002, Date: 124, Chat: gotgbot.Chat{Id: chatID, Type: "private"}}
	require.NoError(t, session.DeliverAIRun(ctx, run.ID, response, restoredResult))
	usage, err := g.GetAIMessageUsage(ctx, chatID, response.MessageId)
	require.NoError(t, err)
	require.Equal(t, int64(80), usage.CachedInputTokens)
	require.Equal(t, int64(1_800_000_000), usage.GeminiCacheExpireTime,
		"usage reads the run snapshot instead of mutable session state")

	state, err := g.GetAISessionRuntimeState(ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, "int-persisted", state.GeminiInteractionID)
	require.Equal(t, "cachedContents/run", state.GeminiCacheName)

	reloadedRow, err := g.AIQ.GetAISession(ctx, stored.ID)
	require.NoError(t, err)
	reloaded := &GeminiSession{GeminiSession: sessionFromV2(reloadedRow)}
	require.NoError(t, reloaded.loadContentFromDatabase(ctx))
	require.NoError(t, reloaded.loadModel(ctx))
	require.Len(t, reloaded.Contents, 2)
	recovered, err := reloaded.GetOrBeginAIRun(ctx, chatID, 5001)
	require.NoError(t, err)
	require.Equal(t, "delivered", recovered.Status)
}

func TestAIRunFailureAndDuplicateDeliveryRemainTraceable(t *testing.T) {
	ctx := context.Background()
	stored := createV2TestSession(t, -920002, ProviderDeepSeek, ModelDeepSeekFlash)
	session := &GeminiSession{
		GeminiSession: stored, Provider: ProviderDeepSeek, Model: ModelDeepSeekFlash,
		TmpContents: []q.GeminiContent{v2TestUserContent(stored.ID, -920002, 6001, "hello")},
	}
	run, err := session.BeginAIRun(ctx)
	require.NoError(t, err)
	modelErr := errors.New("provider unavailable")
	require.NoError(t, MarkAIRunFailed(ctx, run.ID, "model_failed", "provider", modelErr))
	failed, err := g.AIQ.GetAIRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "model_failed", failed.Status)
	require.Equal(t, modelErr.Error(), failed.ErrorMessage.String)
	savedSession, err := g.AIQ.GetAISession(ctx, stored.ID)
	require.NoError(t, err)
	require.Zero(t, savedSession.TotalInputTokens)
	session.DiscardTmpUpdates()
	require.Len(t, session.Contents, 1, "durably saved input remains in the cached session after failure")

	stored = createV2TestSession(t, -920003, ProviderDeepSeek, ModelDeepSeekFlash)
	session = &GeminiSession{
		GeminiSession: stored, Provider: ProviderDeepSeek, Model: ModelDeepSeekFlash,
		TmpContents: []q.GeminiContent{v2TestUserContent(stored.ID, -920003, 6101, "hello")},
	}
	run, err = session.BeginAIRun(ctx)
	require.NoError(t, err)
	result := &AIResult{
		DisplayText: "answer", AssistantPayload: []byte(`{"role":"assistant","content":"answer"}`),
		AssistantPayloadFormat: PayloadFormatDeepSeekMessage,
		Usage:                  AIUsage{InputTokens: 10, OutputTokens: 2}, InputMessageCount: 1,
		InputFirstMsgID: 6101, InputLastMsgID: 6101,
	}
	require.NoError(t, session.CompleteAIRun(ctx, run.ID, result))
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot"}}
	t.Cleanup(func() { mainBot = previousBot })
	response := &gotgbot.Message{MessageId: 6102, Date: 124, Chat: gotgbot.Chat{Id: -920003, Type: "private"}}
	require.NoError(t, session.DeliverAIRun(ctx, run.ID, response, result))
	require.Error(t, session.DeliverAIRun(ctx, run.ID, response, result),
		"a duplicate delivery cannot create another response or advance state twice")
	messages, err := g.AIQ.ListAISessionMessages(ctx, stored.ID)
	require.NoError(t, err)
	require.Len(t, messages, 2)
}

func TestAIRunDatabaseFailureRollsBackGeneratedStateAndUsage(t *testing.T) {
	ctx := context.Background()
	stored := createV2TestSession(t, -920005, ProviderGemini, ModelGeminiFlash)
	session := &GeminiSession{
		GeminiSession: stored, Provider: ProviderGemini, Model: ModelGeminiFlash,
		TmpContents: []q.GeminiContent{v2TestUserContent(stored.ID, -920005, 6201, "hello")},
	}
	run, err := session.BeginAIRun(ctx)
	require.NoError(t, err)
	err = session.CompleteAIRun(ctx, run.ID, &AIResult{
		DisplayText: "invalid usage", Usage: AIUsage{InputTokens: -1},
		InputMessageCount: 1, InputFirstMsgID: 6201, InputLastMsgID: 6201,
	})
	require.Error(t, err)
	savedRun, lookupErr := g.AIQ.GetAIRun(ctx, run.ID)
	require.NoError(t, lookupErr)
	require.Equal(t, "pending", savedRun.Status, "the generated transition is atomic")
	savedSession, lookupErr := g.AIQ.GetAISession(ctx, stored.ID)
	require.NoError(t, lookupErr)
	require.Zero(t, savedSession.TotalInputTokens, "failed transactions cannot partially count Token usage")
	require.NoError(t, MarkAIRunFailed(ctx, run.ID, "persistence_failed", "constraint", err))
	savedRun, lookupErr = g.AIQ.GetAIRun(ctx, run.ID)
	require.NoError(t, lookupErr)
	require.Equal(t, "persistence_failed", savedRun.Status)
}

func TestSameTelegramMessageCanBranchIntoMultipleSessions(t *testing.T) {
	ctx := context.Background()
	const chatID int64 = -920004
	first := createV2TestSession(t, chatID, ProviderGemini, ModelGeminiFlash)
	second := createV2TestSession(t, chatID, ProviderDeepSeek, ModelDeepSeekFlash)
	shared := v2TestUserContent(first.ID, chatID, 7001, "shared")
	firstSession := &GeminiSession{GeminiSession: first, Provider: ProviderGemini, Model: ModelGeminiFlash,
		TmpContents: []q.GeminiContent{shared}}
	_, err := firstSession.BeginAIRun(ctx)
	require.NoError(t, err)
	shared.SessionID = second.ID
	secondSession := &GeminiSession{GeminiSession: second, Provider: ProviderDeepSeek, Model: ModelDeepSeekFlash,
		TmpContents: []q.GeminiContent{shared}}
	_, err = secondSession.BeginAIRun(ctx)
	require.NoError(t, err)
	firstMessages, err := g.AIQ.ListAISessionMessages(ctx, first.ID)
	require.NoError(t, err)
	secondMessages, err := g.AIQ.ListAISessionMessages(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, int64(7001), firstMessages[0].MsgID)
	require.Equal(t, int64(7001), secondMessages[0].MsgID)
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
