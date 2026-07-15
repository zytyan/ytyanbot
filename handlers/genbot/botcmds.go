package genbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	g "main/globalcfg"
	"strconv"
	"strings"
	"time"
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

func modelKeyboard(current string, targetSessionID ...int64) gotgbot.InlineKeyboardMarkup {
	sessionID := int64(0)
	if len(targetSessionID) > 0 {
		sessionID = targetSessionID[0]
	}
	rows := make([][]gotgbot.InlineKeyboardButton, 0, len(modelOptions))
	for _, option := range modelOptions {
		label := option.Label
		if option.Model == current {
			label = "✅ " + label
		}
		rows = append(rows, []gotgbot.InlineKeyboardButton{{
			Text: label, CallbackData: modelCallbackData(sessionID, option.Model),
		}})
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func modelCallbackData(sessionID int64, model string) string {
	return fmt.Sprintf("%s%d:%s", modelCallbackPrefix, sessionID, model)
}

func parseModelCallback(data string) (sessionID int64, model string, ok bool) {
	if !strings.HasPrefix(data, modelCallbackPrefix) {
		return 0, "", false
	}
	payload := strings.TrimPrefix(data, modelCallbackPrefix)
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) == 1 {
		// Keep buttons sent by older bot versions working.
		return 0, parts[0], parts[0] != ""
	}
	sessionID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || sessionID < 0 || parts[1] == "" {
		return 0, "", false
	}
	return sessionID, parts[1], true
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
	requestCtx := context.Background()
	sessionID := int64(0)
	model, err := g.GetAIChatModel(requestCtx, msg.Chat.Id, defaultAIModel)
	if err != nil {
		return err
	}
	prompt := "请选择当前聊天使用的 AI 模型："
	if msg.ReplyToMessage != nil {
		sessionID, err = g.AIQ.GetAISessionIDByMessage(requestCtx, msg.Chat.Id, msg.ReplyToMessage.MessageId)
		if errors.Is(err, sql.ErrNoRows) {
			_, replyErr := msg.Reply(bot, "被回复的消息不属于可切换的 AI 会话。", nil)
			return replyErr
		}
		if err != nil {
			return err
		}
		_, model, err = g.GetAISessionModel(requestCtx, sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			model, err = g.GetAIChatModel(requestCtx, msg.Chat.Id, defaultAIModel)
			if err == nil {
				err = g.SetAISessionModel(requestCtx, sessionID, providerForModel(model), model)
			}
		}
		if err != nil {
			return err
		}
		prompt = fmt.Sprintf("请选择历史会话 #%d 使用的 AI 模型：", sessionID)
	}
	_, err = msg.Reply(bot, prompt, &gotgbot.SendMessageOpts{
		ReplyMarkup: modelKeyboard(model, sessionID),
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
	sessionID, model, validCallback := parseModelCallback(ctx.CallbackQuery.Data)
	if !validCallback {
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "无效模型切换请求", ShowAlert: true})
		return errors.New("invalid model callback payload")
	}
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
	requestCtx := context.Background()
	if sessionID != 0 {
		target, targetErr := g.AIQ.GetAISession(requestCtx, sessionID)
		if targetErr != nil || target.ChatID != msg.Chat.Id {
			_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
				Text: "目标历史会话不存在或不属于当前聊天", ShowAlert: true,
			})
			if targetErr != nil && !errors.Is(targetErr, sql.ErrNoRows) {
				return targetErr
			}
			return nil
		}
		_, currentModel, modelErr := g.GetAISessionModel(requestCtx, sessionID)
		if modelErr != nil {
			return modelErr
		}
		if currentModel == model {
			_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "该会话已使用 " + option.Label})
			return nil
		}
		if err = g.ChangeAISessionModel(requestCtx, sessionID, option.Provider, model); err != nil {
			return err
		}
		invalidateSession(sessionID)
		text := fmt.Sprintf("历史会话 #%d 已切换到 %s；下次继续该会话时将从本地文本历史重建，不重放旧远端链。", sessionID, option.Label)
		_, _, editErr := msg.EditText(bot, text,
			&gotgbot.EditMessageTextOpts{ReplyMarkup: modelKeyboard(model, sessionID)})
		_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "已切换到 " + option.Label})
		return editErr
	}
	if err = g.SetAIChatModel(requestCtx, msg.Chat.Id, option.Provider, model); err != nil {
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
	enabled, err := g.ToggleAIChatUsage(context.Background(), msg.Chat.Id,
		providerForModel(defaultAIModel), defaultAIModel)
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

func formatExplicitCacheExpireTime(expireTime int64) string {
	if expireTime == 0 {
		return "无显式缓存"
	}
	return time.Unix(expireTime, 0).In(shanghaiLocation).Format("2006-01-02 15:04:05")
}

func formatUsageAlert(usage g.AIMessageUsage) string {
	messageSummary := "未记录"
	if usage.InputMessageCount > 0 {
		messageSummary = fmt.Sprintf("%d（%d-%d）", usage.InputMessageCount,
			usage.InputFirstMsgID, usage.InputLastMsgID)
	}
	text := fmt.Sprintf("ID: %d\n消息: %s\n模型: %s\n输入: %s\n输出: %s\n缓存: %s\n缓存到期：%s",
		usage.SessionID, messageSummary,
		usage.Model, formatKTokens(usage.InputTokens), formatKTokens(usage.OutputTokens),
		formatKTokens(usage.CachedInputTokens), formatExplicitCacheExpireTime(usage.GeminiCacheExpireTime))
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
