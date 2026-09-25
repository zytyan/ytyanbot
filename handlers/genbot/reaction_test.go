package genbot

import (
	"context"
	"encoding/json"
	"testing"

	g "main/globalcfg"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/stretchr/testify/require"
)

type reactionCaptureClient struct {
	methods []string
}

func (c *reactionCaptureClient) RequestWithContext(_ context.Context, _ string, method string,
	_ map[string]any, _ *gotgbot.RequestOpts,
) (json.RawMessage, error) {
	c.methods = append(c.methods, method)
	return json.RawMessage(`true`), nil
}

func (*reactionCaptureClient) GetAPIURL(*gotgbot.RequestOpts) string { return "https://example.test" }
func (*reactionCaptureClient) FileURL(string, string, *gotgbot.RequestOpts) string {
	return "https://example.test/file"
}

func TestSetReactionRespectsPreference(t *testing.T) {
	client := &reactionCaptureClient{}
	bot := &gotgbot.Bot{BotClient: client}
	msg := &gotgbot.Message{MessageId: 7, Chat: gotgbot.Chat{Id: -1001, Type: "supergroup"}}

	setReaction(false, bot, msg, "👀")
	require.Empty(t, client.methods)

	setReaction(true, bot, msg, "👀")
	require.Equal(t, []string{"setMessageReaction"}, client.methods)
}

func TestConfigureAIReactions(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 940001

	text, err := configureAIReactions(ctx, userID, []string{"/ai_reaction"})
	require.NoError(t, err)
	require.Contains(t, text, "当前为开启")

	text, err = configureAIReactions(ctx, userID, []string{"/ai_reaction", "off"})
	require.NoError(t, err)
	require.Equal(t, "已关闭 AI emoji reaction。", text)
	text, err = configureAIReactions(ctx, userID, []string{"/ai_reaction", "off"})
	require.NoError(t, err)
	require.Equal(t, "已关闭 AI emoji reaction。", text)
	enabled, err := g.GetAIUserReactionsEnabled(ctx, userID)
	require.NoError(t, err)
	require.False(t, enabled)

	text, err = configureAIReactions(ctx, userID, []string{"/ai_reaction", "invalid"})
	require.NoError(t, err)
	require.Contains(t, text, "当前为关闭")
	enabled, err = g.GetAIUserReactionsEnabled(ctx, userID)
	require.NoError(t, err)
	require.False(t, enabled)

	text, err = configureAIReactions(ctx, userID, []string{"/ai_reaction", "ON"})
	require.NoError(t, err)
	require.Equal(t, "已开启 AI emoji reaction。", text)
}
