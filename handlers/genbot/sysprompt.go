package genbot

import (
	"context"
	"database/sql"
	"errors"
	g "main/globalcfg"
	"main/globalcfg/h"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func systemPromptFromMessage(msg *gotgbot.Message) string {
	prompt := h.TrimCmd(msg.GetText())
	if prompt != "" {
		return prompt
	}
	if msg.ReplyToMessage == nil {
		return ""
	}
	prompt = msg.ReplyToMessage.GetText()
	if strings.TrimSpace(prompt) == "" {
		return ""
	}
	return prompt
}

func UpdateGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	topic := newTopic(msg)
	prompt := systemPromptFromMessage(msg)
	if prompt == "" {
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
	err := g.AIQ.UpsertAISystemPrompt(context.Background(), topic.chatId, topic.topicId, prompt, time.Now().Unix())
	if err != nil {
		_, replyErr := msg.Reply(bot, "设置系统提示词错误: "+err.Error(), nil)
		return errors.Join(err, replyErr)
	}
	invalidateSysPrompt(topic)
	_, err = msg.Reply(bot, "成功设置系统提示词:\n"+prompt, nil)
	return err
}

func ResetGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	topic := newTopic(msg)
	err := g.AIQ.DeleteAISystemPrompt(context.Background(), topic.chatId, topic.topicId)
	if err != nil {
		_, replyErr := msg.Reply(bot, err.Error(), nil)
		return errors.Join(err, replyErr)
	}
	invalidateSysPrompt(topic)
	_, err = msg.Reply(bot, "已恢复默认提示词", nil)
	return err
}

func GetGeminiSysPrompt(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	topic := newTopic(msg)
	prompt, err := g.AIQ.GetAISystemPrompt(context.Background(), topic.chatId, topic.topicId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && prompt == "") {
		prompt = gDefaultSysPrompt
	} else if err != nil {
		_, replyErr := msg.Reply(bot, "读取系统提示词错误: "+err.Error(), nil)
		return errors.Join(err, replyErr)
	}
	_, err = msg.Reply(bot, prompt, nil)
	return err
}
