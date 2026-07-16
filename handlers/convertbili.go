package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"html"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/q"
	"main/helpers/bili"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

var log = g.GetLogger("handlers", slog.LevelInfo)
var logD = log

func chatCfg(id int64) *q.ChatCfg {
	return g.Q.GetChatCfgByIdOrDefault(id)
}

func BiliMsgFilter(msg *gotgbot.Message) bool {
	if !chatCfg(msg.Chat.Id).AutoCvtBili {
		return false
	}
	if msg.ViaBot != nil || strings.HasPrefix(msg.Text, "/") {
		return false
	}
	text := msg.GetText()
	return strings.Contains(text, "b23.tv") ||
		strings.Contains(text, "bilibili.com") ||
		strings.Contains(text, "bili2233.cn")
}

func BiliMsgConverter(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	prepare, err := bili.ConvertBilibiliLinks(ctx.Message.Text)
	if err != nil || !prepare.NeedClean {
		logD.Info("bilibili links error", "err", err, "text", ctx.Message.Text)
		// 没有B站链接
		return nil
	}
	logD.Info("convert bilibili link", "text", ctx.Message.Text)

	bv := html.EscapeString(prepare.BvText)
	text := fmt.Sprintf(`来自<a href="tg://user?id=%d">%s</a>的消息，其中的链接已自动转换%s%s`,
		ctx.EffectiveSender.Id(),
		ctx.EffectiveSender.Name(),
		"\n\n",
		bv)
	_, err = bot.SendMessage(ctx.Message.Chat.Id, text, &gotgbot.SendMessageOpts{ParseMode: `HTML`,
		ReplyMarkup: &gotgbot.InlineKeyboardMarkup{
			InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
				Text:         "下载视频",
				CallbackData: biliCallbackData,
			}}},
		},
	})
	if err != nil {
		log.Info("convert bilibili link send message failed", "text", text, "err", err)
		return err
	}
	_, err = ctx.Message.Delete(bot, nil)

	if err != nil {
		log.Info("convert bilibili link delete original message failed", "text", ctx.Message.Text, "err", err)
	}
	return err
}

func buildQueryResult(title, text string, markups [][]gotgbot.InlineKeyboardButton) gotgbot.InlineQueryResult {
	fnvhash := fnv.New128a()
	_, _ = fnvhash.Write([]byte(title + text))
	bytes := fnvhash.Sum(nil)
	id := base64.URLEncoding.EncodeToString(bytes)
	return &gotgbot.InlineQueryResultArticle{
		Id:    id,
		Title: title,
		InputMessageContent: &gotgbot.InputTextMessageContent{
			MessageText: text,
		},
		ReplyMarkup: &gotgbot.InlineKeyboardMarkup{
			InlineKeyboard: markups,
		},
	}
}

type BiliInlineResult struct {
	Uid     int64 `gorm:"primaryKey"`
	Text    string
	ChatId  int64
	Message int64
}

const biliCallbackData = "download:bilibili"

func BiliMsgConverterInline(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	if ctx.InlineQuery.Query == "" {
		_, err := ctx.InlineQuery.Answer(bot, []gotgbot.InlineQueryResult{
			buildQueryResult("这个家伙什么也没有说", "这个家伙什么也没有说", nil)}, nil)
		return err
	}
	log.Debug("convert bilibili link", "query", ctx.InlineQuery.Query)
	links, err := bili.ConvertBilibiliLinks(ctx.InlineQuery.Query)
	if err != nil || !links.CanConvert() {
		_, err := ctx.InlineQuery.Answer(bot,
			[]gotgbot.InlineQueryResult{
				buildQueryResult("没有检测到可转换的Bilibili链接或出现错误，点击可原样发送", ctx.InlineQuery.Query, nil)},
			nil)
		return err
	}

	uid, err := g.Q.CreateBiliInlineData(context.Background(), q.UnixTime{Time: time.Now()})
	if err != nil {
		logD.Warn("insert to bilibili error", "err", err)
		return err
	}
	callbackData := biliInlineCallbackPrefix + strconv.FormatInt(uid, 16)
	var btns [][]gotgbot.InlineKeyboardButton
	if bili.HasVideoLink(links.BvText) {
		btns = append(btns, []gotgbot.InlineKeyboardButton{{
			Text:         "下载视频",
			CallbackData: callbackData,
		}})
	}
	var results []gotgbot.InlineQueryResult
	results = append(results, buildQueryResult("转换后的链接", links.BvText, btns))
	if links.HasAv {
		results = append(results, buildQueryResult("转换后的链接（使用AV号）", links.AvText, btns))
	}
	_, err = ctx.InlineQuery.Answer(bot, results, nil)

	return err
}
func getBiliCallbackDataInMsg(ctx *ext.Context) (uid int64) {
	msg := ctx.EffectiveMessage
	for _, row := range msg.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, biliInlineCallbackPrefix) {
				uid, _ = strconv.ParseInt(btn.CallbackData[len(biliInlineCallbackPrefix):], 16, 64)
				return uid
			}
		}
	}
	log.Error("no bili inline button", "chat_title", msg.Chat.Title, "chat_id", msg.Chat.Id, "message_id", msg.MessageId)
	panic(fmt.Sprintf("%s(%d)/%dno bili inline button", msg.Chat.Title, msg.Chat.Id, msg.MessageId))
}
func SaveBiliMsgCallbackMsgId(_ *gotgbot.Bot, ctx *ext.Context) (err error) {
	uid := getBiliCallbackDataInMsg(ctx)
	msg := ctx.EffectiveMessage
	c, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	return g.Q.UpdateBiliInlineMsgId(c, msg.Text, msg.Chat.Id, msg.MessageId, uid)
}

func IsBilibiliBtn(cq *gotgbot.CallbackQuery) bool {
	return cq.Data == biliCallbackData
}

const biliInlineCallbackPrefix = "il:bili:"

func IsBilibiliInlineBtn2(msg *gotgbot.Message) bool {
	if msg.ViaBot == nil || msg.ViaBot.Id != GetMainBot().Id {
		return false
	}
	if msg.ReplyMarkup == nil || len(msg.ReplyMarkup.InlineKeyboard) == 0 {
		return false
	}
	for _, row := range msg.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, biliInlineCallbackPrefix) {
				return true
			}
		}
	}
	return false
}
func IsBilibiliInlineBtn(cq *gotgbot.CallbackQuery) bool {
	return strings.HasPrefix(cq.Data, biliInlineCallbackPrefix)
}
