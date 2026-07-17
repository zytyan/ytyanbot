package genbot

import (
	"context"
	"encoding/json"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func sendAIRichMessage(ctx context.Context, bot *gotgbot.Bot, replyTo *gotgbot.Message,
	markdown string, replyMarkup gotgbot.ReplyMarkup,
) (*gotgbot.Message, error) {
	params := map[string]any{
		"chat_id": replyTo.Chat.Id,
		"rich_message": map[string]any{
			"markdown": markdown,
		},
		"reply_parameters": gotgbot.ReplyParameters{MessageId: replyTo.MessageId},
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}

	raw, err := bot.RequestWithContext(ctx, "sendRichMessage", params, nil)
	if err != nil {
		return nil, err
	}
	var msg gotgbot.Message
	if err = json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
