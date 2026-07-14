package genbot

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

type ReplaceCtx struct {
	Now time.Time
	Bot *gotgbot.Bot
	Msg *gotgbot.Message

	Memories []string
	Stable   bool
}

func dynamicValue(ctx *ReplaceCtx, name, value string) string {
	if ctx.Stable {
		return "%" + name + "%"
	}
	return value
}

func getChatName(chat gotgbot.Chat) string {
	if chat.Title != "" {
		return chat.Title
	}
	if chat.LastName != "" {
		return chat.FirstName + " " + chat.LastName
	}
	if chat.FirstName != "" {
		return chat.FirstName
	}
	return chat.Username
}

var replaceMetaVar = map[string]func(ctx *ReplaceCtx) string{
	// 使用 %VAR% 替换
	//%TIME% => 15:04:05，下同
	"TIME": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "TIME", ctx.Now.Format("15:04:05"))
	},
	"DATE": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "DATE", ctx.Now.Format("2006-01-02"))
	},
	"DATETIME": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "DATETIME", ctx.Now.Format("2006-01-02 15:04:05"))
	},
	"DATETIME_TZ": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "DATETIME_TZ", ctx.Now.Format("2006-01-02 15:04:05 -07:00"))
	},
	"WEEKDAY": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "WEEKDAY", ctx.Now.Format("Mon"))
	},
	"CHAT_NAME": func(ctx *ReplaceCtx) string {
		chat := ctx.Msg.GetChat()
		if chat.Title != "" {
			return chat.Title
		}
		return getChatName(ctx.Msg.Chat)
	},
	"BOT_NAME": func(ctx *ReplaceCtx) string {
		if ctx.Bot.LastName != "" {
			return ctx.Bot.FirstName + " " + ctx.Bot.LastName
		}
		return ctx.Bot.FirstName
	},
	"BOT_USERNAME": func(ctx *ReplaceCtx) string {
		return ctx.Bot.Username
	},
	"CHAT_TYPE": func(ctx *ReplaceCtx) string {
		return ctx.Msg.Chat.Type
	},
	"MSG_ID": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "MSG_ID", strconv.FormatInt(ctx.Msg.MessageId, 10))
	},
	"CHAT_ID": func(ctx *ReplaceCtx) string {
		return strconv.FormatInt(ctx.Msg.Chat.Id, 10)
	},
	"SENDER_NAME": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "SENDER_NAME", ctx.Msg.GetSender().Name())
	},
	"SENDER_USERNAME": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "SENDER_USERNAME", ctx.Msg.GetSender().Username())
	},
	"SENDER_ID": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "SENDER_ID", strconv.FormatInt(ctx.Msg.GetSender().Id(), 10))
	},
	"MSG_DATETIME": func(ctx *ReplaceCtx) string {
		return dynamicValue(ctx, "MSG_DATETIME", time.Unix(ctx.Msg.Date, 0).Format("2006-01-02 15:04:05"))
	},
	"QUOTE": func(ctx *ReplaceCtx) string {
		if ctx.Stable {
			return "%QUOTE%"
		}
		if ctx.Msg.Quote == nil {
			return ""
		}
		return ctx.Msg.Quote.Text
	},
	"MEMORIES": func(ctx *ReplaceCtx) string {
		if len(ctx.Memories) == 0 {
			return "当前没有记忆"
		}
		buf := bytes.NewBuffer(nil)
		for i, m := range ctx.Memories {
			buf.WriteString(strconv.FormatInt(int64(i)+1, 10))
			buf.WriteString(". ")
			buf.WriteString(m)
		}
		return buf.String()
	},
}

type Replacer struct {
	replaceFunc []func(ctx *ReplaceCtx) string
}

func (r *Replacer) Replace(ctx *ReplaceCtx) string {
	if ctx == nil || ctx.Bot == nil || ctx.Msg == nil {
		return ""
	}
	buf := make([]string, 0, len(r.replaceFunc))
	for _, fn := range r.replaceFunc {
		buf = append(buf, fn(ctx))
	}
	return strings.Join(buf, "")
}
func isValidVarName(s string) bool {
	// 强约束：全大写 + 数字 + 下划线
	// 这样用户不会写出 %chat_name% 这种隐性错误
	for _, r := range s {
		if r == '_' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		// 仅 A-Z
		if r < 'A' || r > 'Z' {
			// 也可以选择 unicode.IsUpper，但会允许非 ASCII 大写，不建议
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return false
			}
			return false
		}
	}
	return true
}

func NewReplacer(tpl string) Replacer {
	if tpl == "" {
		return Replacer{}
	}
	partsLen := strings.Count(tpl, "%")/2 + 1
	parts := make([]func(ctx *ReplaceCtx) string, 0, partsLen)

	emitConst := func(s string) {
		if s == "" {
			return
		}
		parts = append(parts, func(*ReplaceCtx) string { return s })
	}

	// 解析规则：
	// - 普通文本原样输出
	// - %VAR% 替换
	// - %% 输出字面量 %
	// - 变量名必须是 [A-Z0-9_]+，否则当普通文本处理
	// - 未知变量默认保留原样（%UNKNOWN%）
	n := len(tpl)
	i := 0
	lastText := 0

	for i < n {
		if tpl[i] != '%' {
			i++
			continue
		}

		// 遇到 '%': 先把前面的普通文本吐出
		emitConst(tpl[lastText:i])

		// 处理 %%
		if i+1 < n && tpl[i+1] == '%' {
			emitConst("%")
			i += 2
			lastText = i
			continue
		}

		// 尝试找 %VAR%
		j := i + 1
		if j >= n {
			// 结尾单个 '%'，当文本
			emitConst("%")
			i++
			lastText = i
			continue
		}

		// 变量名至少一个字符，直到遇到下一个 '%'
		start := j
		for j < n && tpl[j] != '%' {
			j++
		}
		if j >= n {
			// 没有闭合 '%'，当文本
			emitConst(tpl[i:])
			i = n
			lastText = n
			break
		}

		// candidate 是 VAR
		varName := tpl[start:j]
		if varName == "" || !isValidVarName(varName) {
			// 不合法：把这一段按文本输出（包含闭合 %）
			emitConst(tpl[i : j+1])
			i = j + 1
			lastText = i
			continue
		}

		// 合法变量名：查白名单
		if fn, ok := replaceMetaVar[varName]; ok {
			parts = append(parts, fn)
		} else {
			// 未知变量：保留原样，便于用户发现写错
			raw := tpl[i : j+1]
			emitConst(raw)
		}

		i = j + 1
		lastText = i
	}

	// 末尾剩余文本
	if lastText < n {
		emitConst(tpl[lastText:])
	}

	return Replacer{replaceFunc: parts}
}
