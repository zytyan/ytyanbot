package genbot

import (
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func testCtx(chat gotgbot.Chat) *ReplaceCtx {
	return &ReplaceCtx{
		Bot: &gotgbot.Bot{User: gotgbot.User{FirstName: "Mars", LastName: "Bot", Username: "marsbot"}},
		Msg: &gotgbot.Message{Chat: chat},
		Now: time.Date(2024, 3, 14, 15, 9, 26, 0, time.UTC),
	}
}

func TestReplacerEmptyTemplate(t *testing.T) {
	r := NewReplacer("")
	if got := r.Replace(nil); got != "" {
		t.Fatalf("expected empty result, got %q", got)
	}
}

func TestReplacerEscapesAndLiterals(t *testing.T) {
	tpl := "rate 100%% ok"
	r := NewReplacer(tpl)
	if got := r.Replace(testCtx(gotgbot.Chat{})); got != "rate 100% ok" {
		t.Fatalf("unexpected replace result: %q", got)
	}

	tpl = "%%TIME%"
	r = NewReplacer(tpl)
	if got := r.Replace(testCtx(gotgbot.Chat{})); got != "%TIME%" {
		t.Fatalf("unexpected escape result: %q", got)
	}

	tpl = "hello %TIME"
	r = NewReplacer(tpl)
	if got := r.Replace(testCtx(gotgbot.Chat{})); got != "hello %TIME" {
		t.Fatalf("unexpected unclosed result: %q", got)
	}
}

func TestReplacerUnknownAndInvalidVars(t *testing.T) {
	ctx := testCtx(gotgbot.Chat{})
	tpl := "%UNKNOWN% %chat_name% %A-B% %TIME%"
	r := NewReplacer(tpl)
	if got := r.Replace(ctx); got != "%UNKNOWN% %chat_name% %A-B% 见最新用户消息头中的时间（Asia/Shanghai）" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestReplacerMetaVars(t *testing.T) {
	ctx := testCtx(gotgbot.Chat{Title: "Test Group"})
	tpl := "chat:%CHAT_NAME% bot:%BOT_NAME% user:%BOT_USERNAME% time:%TIME% date:%DATE% dt:%DATETIME% dtz:%DATETIME_TZ%"
	r := NewReplacer(tpl)
	if got := r.Replace(ctx); got != "chat:Test Group bot:Mars Bot user:marsbot time:见最新用户消息头中的时间（Asia/Shanghai） date:见最新用户消息头中的日期（Asia/Shanghai） dt:见最新用户消息头中的日期和时间（Asia/Shanghai） dtz:见最新用户消息头中的日期和时间（Asia/Shanghai）" {
		t.Fatalf("unexpected result: %q", got)
	}

	ctx = testCtx(gotgbot.Chat{FirstName: "Alice", LastName: "Doe", Username: "alice"})
	tpl = "chat:%CHAT_NAME%"
	r = NewReplacer(tpl)
	if got := r.Replace(ctx); got != "chat:Alice Doe" {
		t.Fatalf("unexpected chat name: %q", got)
	}

	ctx = testCtx(gotgbot.Chat{FirstName: "Solo", Username: "solo"})
	tpl = "chat:%CHAT_NAME%"
	r = NewReplacer(tpl)
	if got := r.Replace(ctx); got != "chat:Solo" {
		t.Fatalf("unexpected chat name with first name only: %q", got)
	}
}

func TestReplacerGeminiSysPromptFormat(t *testing.T) {
	ctx := testCtx(gotgbot.Chat{Title: "Test Group", Type: "group"})
	tpl := "现在是:%DATETIME_TZ%\n这里是一个Telegram聊天 type:%CHAT_TYPE%,name:%CHAT_NAME%\n" +
		"你是一个Telegram机器人，name: %BOT_NAME% username: %BOT_USERNAME%"
	r := NewReplacer(tpl)
	got := r.Replace(ctx)
	want := "现在是:见最新用户消息头中的日期和时间（Asia/Shanghai）\n这里是一个Telegram聊天 type:group,name:Test Group\n你是一个Telegram机器人，name: Mars Bot username: marsbot"
	if got != want {
		t.Fatalf("unexpected gemini prompt output: %q", got)
	}
}
