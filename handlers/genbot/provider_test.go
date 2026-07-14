package genbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	g "main/globalcfg"
	"main/globalcfg/q"
	"net/http"
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
	photo, ok := deepSeekContent(ptr(testContent("photo", "caption")))
	require.True(t, ok)
	require.Contains(t, photo.Content, "caption")
	require.Contains(t, photo.Content, "[图片]")

	sticker, ok := deepSeekContent(ptr(testContent("sticker", "🙂")))
	require.True(t, ok)
	require.Contains(t, sticker.Content, "🙂")

	_, ok = deepSeekContent(ptr(testContent("video", "")))
	require.False(t, ok)
	videoCaption, ok := deepSeekContent(ptr(testContent("video", "only caption")))
	require.True(t, ok)
	require.Contains(t, videoCaption.Content, "only caption")
	require.NotContains(t, videoCaption.Content, "[视频]")
}

func TestDeepSeekPureVideoRejected(t *testing.T) {
	session := &GeminiSession{TmpContents: []q.GeminiContent{testContent("video", "")}}
	_, err := session.ToDeepSeekMessages("system", "turn")
	require.ErrorIs(t, err, ErrDeepSeekVideoOnly)
}

func TestCallDeepSeek(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":20,"prompt_cache_hit_tokens":12,"prompt_cache_miss_tokens":8,"completion_tokens":3,"completion_tokens_details":{"reasoning_tokens":1}}}`)),
		}, nil
	})}

	result, err := callDeepSeek(context.Background(), client, "https://example.test", "secret", deepSeekRequest{
		Model: ModelDeepSeekFlash, Messages: []deepSeekMessage{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Text)
	require.Equal(t, int64(12), result.Usage.CachedInputTokens)
	require.Equal(t, int64(1), result.Usage.ThinkingTokens)
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

func TestStablePromptKeepsDynamicPlaceholder(t *testing.T) {
	r := NewReplacer("time=%DATETIME_TZ% chat=%CHAT_NAME% quote=%QUOTE%")
	ctx := &ReplaceCtx{
		Stable: true,
		Now:    time.Unix(123, 0),
		Bot:    &gotgbot.Bot{},
		Msg:    &gotgbot.Message{Chat: gotgbot.Chat{Title: "group"}},
	}
	got := r.Replace(ctx)
	require.Contains(t, got, "%DATETIME_TZ%")
	require.Contains(t, got, "%QUOTE%")
	require.Contains(t, got, "group")
}

func ptr[T any](value T) *T { return &value }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestModelKeyboardMarksCurrent(t *testing.T) {
	keyboard := modelKeyboard(ModelDeepSeekFlash)
	require.Len(t, keyboard.InlineKeyboard, 3)
	require.True(t, strings.HasPrefix(keyboard.InlineKeyboard[2][0].Text, "✅"))
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
		Model: ModelDeepSeekFlash, InputTokens: 27014, OutputTokens: 32, CachedInputTokens: 27008,
	})
	require.Equal(t, "模型: deepseek-v4-flash\n输入: 27.01 ktoken\n输出: 0.03 ktoken\n缓存: 27.01 ktoken", alert)
	require.LessOrEqual(t, len([]rune(alert)), 200)

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
