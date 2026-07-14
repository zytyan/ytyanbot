package genbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	g "main/globalcfg"
	"strings"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const (
	modelCallbackPrefix = "aimodel:"
	usageCallbackData   = "aiusage"
)

func canChangeModel(bot *gotgbot.Bot, chat *gotgbot.Chat, userID int64) (bool, error) {
	if userID == g.GetConfig().God || chat.Type == "private" {
		return true, nil
	}
	member, err := bot.GetChatMember(chat.Id, userID, nil)
	if err != nil {
		return false, err
	}
	status := member.GetStatus()
	return status == "administrator" || status == "creator", nil
}

func modelKeyboard(current string) gotgbot.InlineKeyboardMarkup {
	rows := make([][]gotgbot.InlineKeyboardButton, 0, len(modelOptions))
	for _, option := range modelOptions {
		label := option.Label
		if option.Model == current {
			label = "✅ " + label
		}
		rows = append(rows, []gotgbot.InlineKeyboardButton{{
			Text: label, CallbackData: modelCallbackPrefix + option.Model,
		}})
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func ChangeGeminiModel(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || ctx.EffectiveUser == nil {
		return errors.New("缺少聊天或用户信息")
	}
	ok, err := canChangeModel(bot, &msg.Chat, ctx.EffectiveUser.Id)
	if err != nil {
		return err
	}
	if !ok {
		_, err = msg.Reply(bot, "只有群主或管理员可以切换模型", nil)
		return err
	}
	model, err := g.GetAIChatModel(context.Background(), msg.Chat.Id, defaultAIModel)
	if err != nil {
		return err
	}
	_, err = msg.Reply(bot, "请选择当前聊天使用的 AI 模型：", &gotgbot.SendMessageOpts{
		ReplyMarkup: modelKeyboard(model),
	})
	return err
}

func IsModelCallback(query *gotgbot.CallbackQuery) bool {
	return strings.HasPrefix(query.Data, modelCallbackPrefix)
}

func ChangeModelByButton(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil {
		return errors.New("模型回调缺少消息")
	}
	model := strings.TrimPrefix(ctx.CallbackQuery.Data, modelCallbackPrefix)
	option, ok := getModelOption(model)
	if !ok {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "无效模型", ShowAlert: true})
		return errors.New("invalid model callback")
	}
	allowed, err := canChangeModel(bot, &msg.Chat, ctx.CallbackQuery.From.Id)
	if err != nil {
		return err
	}
	if !allowed {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "只有群主或管理员可以切换模型", ShowAlert: true})
		return nil
	}
	if option.Provider == ProviderDeepSeek && g.GetConfig().DeepSeekKey == "" {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "DeepSeek API Key 未配置", ShowAlert: true})
		return nil
	}
	if err = g.SetAIChatModel(context.Background(), msg.Chat.Id, model); err != nil {
		return err
	}
	invalidateChatSessions(msg.Chat.Id)
	_, _, editErr := msg.EditText(bot, fmt.Sprintf("当前聊天已切换到 %s；下一条消息将开始新会话。", option.Label),
		&gotgbot.EditMessageTextOpts{ReplyMarkup: modelKeyboard(model)})
	_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "已切换到 " + option.Label})
	return editErr
}

func ToggleShowUsage(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || ctx.EffectiveUser == nil {
		return errors.New("缺少聊天或用户信息")
	}
	allowed, err := canChangeModel(bot, &msg.Chat, ctx.EffectiveUser.Id)
	if err != nil {
		return err
	}
	if !allowed {
		_, err = msg.Reply(bot, "只有群主或管理员可以修改用量显示设置", nil)
		return err
	}
	enabled, err := g.ToggleAIChatUsage(context.Background(), msg.Chat.Id, defaultAIModel)
	if err != nil {
		return err
	}
	state := "关闭"
	if enabled {
		state = "开启"
	}
	_, err = msg.Reply(bot, "已"+state+" AI 回复的 Token 用量按钮。", nil)
	return err
}

func usageKeyboard() gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{
		Text: "📊 Token 用量", CallbackData: usageCallbackData,
	}}}}
}

func IsUsageCallback(query *gotgbot.CallbackQuery) bool {
	return query.Data == usageCallbackData
}

func formatKTokens(tokens int64) string {
	return fmt.Sprintf("%.2f ktoken", float64(tokens)/1000)
}

func formatUsageAlert(usage g.AIMessageUsage) string {
	text := fmt.Sprintf("模型: %s\n输入: %s\n输出: %s\n缓存: %s",
		usage.Model, formatKTokens(usage.InputTokens), formatKTokens(usage.OutputTokens),
		formatKTokens(usage.CachedInputTokens))
	if utf8.RuneCountInString(text) <= 200 {
		return text
	}
	runes := []rune(text)
	return string(runes[:199]) + "…"
}

func ShowUsageByButton(bot *gotgbot.Bot, ctx *ext.Context) error {
	return showUsageByButton(bot, ctx, g.GetAIMessageUsage)
}

type usageLookup func(context.Context, int64, int64) (g.AIMessageUsage, error)

func showUsageByButton(bot *gotgbot.Bot, ctx *ext.Context, lookup usageLookup) error {
	msg := ctx.EffectiveMessage
	if msg == nil || ctx.CallbackQuery == nil {
		return errors.New("用量回调缺少消息")
	}
	usage, err := lookup(context.Background(), msg.Chat.Id, msg.MessageId)
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text: "用量记录不存在或尚未写入，请稍后重试。", ShowAlert: true,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
		Text: formatUsageAlert(usage), ShowAlert: true,
	})
	return err
}
