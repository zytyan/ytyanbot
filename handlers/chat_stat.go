package handlers

import (
	"context"
	g "main/globalcfg"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/rivo/uniseg"
)

func StatMessage(bot *gotgbot.Bot, ctx *ext.Context) error {
	_ = bot
	msg := ctx.EffectiveMessage
	chat := ctx.EffectiveChat
	user := ctx.EffectiveUser
	if msg == nil || chat == nil || user == nil {
		return nil
	}
	storedUser, err := g.Q.GetOrCreateUserByTg(context.Background(), user)
	if err != nil {
		log.Warn("save user for chat statistics", "user_id", user.Id, "err", err)
	} else if err = storedUser.TryUpdate(g.Q, user); err != nil {
		log.Warn("update user for chat statistics", "user_id", user.Id, "err", err)
	}
	chatStat := g.Q.ChatStatAt(chat.Id, msg.Date)
	if chatStat == nil {
		return nil
	}
	txtLen := int64(uniseg.GraphemeClusterCount(msg.Text))
	chatStat.IncMessage(user.Id, txtLen, msg.Date, int64(msg.MessageId))
	if msg.Photo != nil {
		chatStat.IncPhotoCount()
	}
	if msg.Video != nil {
		chatStat.IncVideoCount()
	}
	if msg.Sticker != nil {
		chatStat.IncStickerCount()
	}
	if msg.ForwardOrigin != nil {
		chatStat.IncForwardCount()
	}
	return nil
}
