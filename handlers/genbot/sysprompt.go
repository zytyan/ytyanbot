package genbot

import (
	"context"
	g "main/globalcfg"
	"main/globalcfg/h"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func UpdateGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	delete(sysPromptReplacerCache, newTopic(ctx.EffectiveMessage))
	msg := ctx.EffectiveMessage
	text := msg.GetText()
	prompt := h.TrimCmd(text)
	if prompt == "" {
		if msg.ReplyToMessage == nil || msg.ReplyToMessage.GetText() == "" {
			_, err := msg.Reply(bot, `没有找到任何System prompt，请使用 /sysprompt 提示词或使用该命令回复其他消息设置提示词。
您需要使用 /get_sysprompt 获取当前系统提示词， /reset_sysprompt 恢复默认系统提示词。

你可以通过 %VAR% 使用稳定变量。为提高缓存命中率，逐轮变化的信息只出现在最新用户消息的「[ 显示名 时间 ]」消息头中。
CHAT_NAME: 当前聊天的名称
CHAT_ID: 当前聊天 ID
BOT_NAME: Bot的名字
BOT_USERNAME: Bot的username
CHAT_TYPE: 聊天类型(group, private)

旧的 TIME、DATE、DATETIME、DATETIME_TZ、WEEKDAY、MSG_DATETIME、SENDER_NAME 会替换为指向最新消息头的稳定说明；MSG_ID、SENDER_USERNAME、SENDER_ID、QUOTE 会显示为“不可用”。

例：当前聊天为%CHAT_NAME%，请结合最新用户消息头中的发送者和时间解答问题。
`, nil)
			return err
		}
	}
	err := g.Q.CreateOrUpdateGeminiSystemPrompt(context.Background(), msg.Chat.Id, msg.MessageThreadId, prompt)
	if err != nil {
		_, err = msg.Reply(bot, "设置系统提示词错误: "+err.Error(), nil)
		return err
	}
	_, err = msg.Reply(bot, "成功设置系统提示词:\n"+prompt, nil)
	return err
}
func ResetGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	delete(sysPromptReplacerCache, newTopic(ctx.EffectiveMessage))
	err := g.Q.ResetGeminiSystemPrompt(context.Background(), ctx.EffectiveChat.Id, ctx.EffectiveMessage.MessageThreadId)
	if err != nil {
		_, err = ctx.EffectiveMessage.Reply(bot, err.Error(), nil)
		return err
	}
	_, err = ctx.EffectiveMessage.Reply(bot, "已恢复默认提示词", nil)
	return err
}
func GetGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	prompt, err := g.Q.GetGeminiSystemPrompt(context.Background(), ctx.EffectiveChat.Id, ctx.EffectiveMessage.MessageThreadId)
	if err != nil {
		_, err = ctx.EffectiveMessage.Reply(bot, gDefaultSysPrompt, nil)
		return err
	}
	_, err = ctx.EffectiveMessage.Reply(bot, prompt, nil)
	return err
}
