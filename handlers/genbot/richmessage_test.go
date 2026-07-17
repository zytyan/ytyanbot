package genbot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/stretchr/testify/require"
)

type richMessageCaptureClient struct {
	method string
	params map[string]any
}

func (c *richMessageCaptureClient) RequestWithContext(_ context.Context, _ string, method string,
	params map[string]any, _ *gotgbot.RequestOpts,
) (json.RawMessage, error) {
	c.method = method
	c.params = params
	return json.RawMessage(`{"message_id":42,"date":123,"chat":{"id":-1001,"type":"supergroup"}}`), nil
}

func (*richMessageCaptureClient) GetAPIURL(*gotgbot.RequestOpts) string {
	return "https://example.test"
}

func (*richMessageCaptureClient) FileURL(string, string, *gotgbot.RequestOpts) string {
	return "https://example.test/file"
}

func TestSendAIRichMessage(t *testing.T) {
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	replyTo := &gotgbot.Message{
		MessageId: 7,
		Chat:      gotgbot.Chat{Id: -1001, Type: "supergroup"},
	}
	keyboard := usageKeyboard()

	msg, err := sendAIRichMessage(context.Background(), bot, replyTo,
		"| 名称 | 值 |\n|---|---|\n| 公式 | $x^2$ |", keyboard)
	require.NoError(t, err)
	require.EqualValues(t, 42, msg.MessageId)
	require.EqualValues(t, -1001, msg.Chat.Id)
	require.Equal(t, "sendRichMessage", client.method)
	require.EqualValues(t, -1001, client.params["chat_id"])
	require.Equal(t, gotgbot.ReplyParameters{MessageId: 7}, client.params["reply_parameters"])
	require.Equal(t, keyboard, client.params["reply_markup"])
	require.Equal(t, map[string]any{
		"markdown": "| 名称 | 值 |\n|---|---|\n| 公式 | $x^2$ |",
	}, client.params["rich_message"])
}

func TestSendAIRichMessageOmitsEmptyKeyboard(t *testing.T) {
	client := &richMessageCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	replyTo := &gotgbot.Message{MessageId: 8, Chat: gotgbot.Chat{Id: 9, Type: "private"}}

	_, err := sendAIRichMessage(context.Background(), bot, replyTo, "plain", nil)
	require.NoError(t, err)
	require.NotContains(t, client.params, "reply_markup")
}

func TestDefaultPromptAllowsLatex(t *testing.T) {
	require.NotContains(t, gDefaultSysPrompt, "telegram不支持")
	require.NotContains(t, gDefaultSysPrompt, "不要使用latex公式")
	require.Contains(t, gDefaultSysPrompt, "请对大家温柔一些。")
}
