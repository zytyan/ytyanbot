package handlers

import (
	"context"
	"fmt"
	g "main/globalcfg"
	"sort"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func GetRank(bot *gotgbot.Bot, ctx *ext.Context) error {
	stat := g.Q.ChatStatAt(ctx.EffectiveChat.Id, time.Now().Unix())
	if stat == nil {
		_, err := bot.SendMessage(ctx.EffectiveChat.Id, "没有数据", nil)
		return err
	}
	snapshot := stat.Snapshot()
	if len(snapshot.UserMsgStat) == 0 {
		_, err := bot.SendMessage(ctx.EffectiveChat.Id, "没有数据", nil)
		return err
	}
	type userCount struct {
		user  int64
		count int64
	}
	tmp := make([]userCount, 0, len(snapshot.UserMsgStat))
	for u, c := range snapshot.UserMsgStat {
		if c == nil {
			continue
		}
		tmp = append(tmp, userCount{u, c.MsgCount})
	}
	sort.Slice(tmp, func(i, j int) bool {
		return tmp[i].count > tmp[j].count
	})
	res := make([]string, 0, len(tmp))
	for i, v := range tmp {
		if i >= 10 {
			break
		}
		user, err := g.Q.GetUserById(context.Background(), v.user)
		if err != nil {
			res = append(res, fmt.Sprintf("用户%d: %d", v.user, v.count))
			continue
		}
		res = append(res, fmt.Sprintf("%s: %d", user.Name(), v.count))
	}
	if len(tmp) > 10 {
		sum := int64(0)
		for i := 10; i < len(tmp); i++ {
			sum += tmp[i].count
		}
		res = append(res, fmt.Sprintf("其他人: %d", sum))
	}
	text := strings.Join(res, "\n")
	if text == "" {
		text = "没有数据"
	}
	_, err := bot.SendMessage(ctx.EffectiveChat.Id, text, nil)
	return err
}

func SendGroupStat(bot *gotgbot.Bot, ctx *ext.Context) error {
	chatId := ctx.EffectiveChat.Id
	if err := sendChatStat(bot, chatId, time.Now().Add(-24*time.Hour)); err != nil {
		return err
	}
	return sendChatStat(bot, chatId, time.Now())
}
