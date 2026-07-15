package genbot

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/h"
	"main/helpers/mdnormalizer"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"google.golang.org/genai"
)

var reReplyToSession = regexp.MustCompile(`@\d+`)
var mainBot *gotgbot.Bot
var log *slog.Logger
var client = g.NewPtrLinkedCfg(
	func(old, new *g.Config) bool {
		return old.GeminiKey != new.GeminiKey
	},
	func(new *g.Config) *genai.Client {
		ctx := context.Background()
		c, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey:  new.GeminiKey,
			Backend: genai.BackendGeminiAPI,
		})
		if err != nil {
			panic(err)
		}
		return c
	},
)

func getGenAiClient() *genai.Client {
	return client.Get()
}

const (
	geminiSessionContentLimit = 200
	geminiSessionWindowStep   = 50
	geminiMemoriesLimit       = 60
)

type geminiTopic struct {
	chatId  int64
	topicId int64
}

func newTopic(msg *gotgbot.Message) geminiTopic {
	res := geminiTopic{
		chatId: msg.Chat.Id,
	}
	if msg.IsTopicMessage {
		res.topicId = msg.MessageThreadId
	}
	return res
}

func IsGeminiReq(msg *gotgbot.Message) bool {
	text := msg.GetText()
	if strings.HasPrefix(text, "/") {
		return false
	}
	if strings.Contains(text, "@"+mainBot.Username) {
		return true
	}
	if msg.ReplyToMessage != nil {
		return msg.ReplyToMessage.GetSender().Id() == mainBot.Id
	}
	return false
}

//go:embed gemini_sysprompt.txt
var gDefaultSysPrompt string
var geminiSysPromptReplacer = NewReplacer(gDefaultSysPrompt)
var sysPromptReplacerCache = make(map[geminiTopic]*Replacer)
var gMu sync.Mutex

func getSysPrompt(msg *gotgbot.Message) *Replacer {
	gMu.Lock()
	defer gMu.Unlock()
	topic := newTopic(msg)
	if r, ok := sysPromptReplacerCache[topic]; ok {
		return r
	}
	tmpl, err := g.Q.GetGeminiSystemPrompt(context.Background(), topic.chatId, topic.topicId)
	if err == nil {
		r := NewReplacer(tmpl)
		sysPromptReplacerCache[topic] = &r
		return &r
	}
	sysPromptReplacerCache[topic] = &geminiSysPromptReplacer
	return &geminiSysPromptReplacer
}

func GeminiReply(bot *gotgbot.Bot, ctx *ext.Context) error {
	if !slices.Contains(g.GetConfig().AIChats, ctx.EffectiveChat.Id) {
		return nil
	}
	msg := ctx.EffectiveMessage
	topic := newTopic(msg)
	genCtx, cancel := context.WithTimeout(context.Background(), time.Minute*15)
	defer cancel()
	text := msg.GetText()
	ignoreSessionTimeout := false
	replySessionId := int64(0)
	if strings.Contains(text, "@last") {
		ignoreSessionTimeout = true
	} else if found := reReplyToSession.FindString(text); found != "" {
		replySessionId, _ = strconv.ParseInt(found[1:], 10, 64)
	}
	session := GeminiGetSession(genCtx, msg, false, ignoreSessionTimeout, replySessionId)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	promptReplacer := getSysPrompt(msg)
	if promptReplacer.NeedsMemories() && len(session.Memories) == 0 {
		memories, err := g.Q.ListGeminiMemory(genCtx, topic.chatId, topic.topicId, 30)
		if err != nil {
			return err
		}
		session.Memories = memories
	}
	setReaction(bot, msg, "👀")

	sysPromptCtx := ReplaceCtx{
		Bot:    bot,
		Msg:    ctx.EffectiveMessage,
		Now:    time.Now(),
		Stable: true,
	}
	for _, mem := range session.Memories {
		sysPromptCtx.Memories = append(sysPromptCtx.Memories, mem.Content)
	}
	sysPrompt := promptReplacer.Replace(&sysPromptCtx)
	config := &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true},
	}
	if err := session.AddTgMessageWithReply(genCtx, bot, ctx.EffectiveMessage); err != nil {
		return err
	}
	if session.AllowCodeExecution {
		config.Tools[0].CodeExecution = &genai.ToolCodeExecution{}
	}
	defer session.DiscardTmpUpdates()

	actionCancel := h.WithChatAction(bot, "typing", msg.Chat.Id, msg.MessageThreadId, msg.IsTopicMessage)
	defer actionCancel()
	res, err := generateAI(genCtx, session, sysPrompt, config)
	actionCancel()
	if err != nil {
		if errors.Is(err, ErrDeepSeekVideoOnly) {
			setReaction(bot, msg, "🤔")
			_, replyErr := ctx.EffectiveMessage.Reply(bot, err.Error(), nil)
			return replyErr
		}
		setReaction(bot, msg, "😭")
		_, _ = ctx.EffectiveMessage.Reply(bot, fmt.Sprintf("error:%s", err), nil)
		return err
	}
	_ = g.Q.IncrementSessionTokenCounters(
		genCtx,
		res.Usage.InputTokens,
		res.Usage.OutputTokens,
		session.ID,
	)
	_ = g.IncrementAICachedTokens(genCtx, session.ID, res.Usage.CachedInputTokens)
	if res.Usage.InputTokens > 0 {
		log.Info("ai usage", "provider", session.Provider, "model", session.Model,
			"input_tokens", res.Usage.InputTokens, "cached_input_tokens", res.Usage.CachedInputTokens,
			"cache_hit_rate", float64(res.Usage.CachedInputTokens)/float64(res.Usage.InputTokens),
			"output_tokens", res.Usage.OutputTokens)
	}
	aiText := res.DisplayText
	if aiText == "" {
		aiText = "模型没有返回任何信息"
		if res.Feedback != "" {
			aiText += "，原因: " + res.Feedback
		}
		setReaction(bot, msg, "🤯")
	}
	normTxt, err := mdnormalizer.Normalize(aiText)
	var respMsg *gotgbot.Message
	replyOpts := &gotgbot.SendMessageOpts{}
	showUsage, usageErr := g.GetAIChatUsageEnabled(genCtx, msg.Chat.Id)
	if usageErr != nil {
		return usageErr
	}
	if showUsage {
		replyOpts.ReplyMarkup = usageKeyboard()
	}
	if err != nil {
		respMsg, err = ctx.EffectiveMessage.Reply(bot, aiText, replyOpts)
		log.Warn("parse markdown failed", "err", err)
	} else {
		replyOpts.Entities = normTxt.Entities
		respMsg, err = ctx.EffectiveMessage.Reply(bot, normTxt.Text, replyOpts)
	}
	if err != nil {
		log.Warn("ai response", "resp", string(res.Raw), "err", err)
		return err
	}
	session.AddModelMessage(respMsg, res)
	if err = session.PersistTmpUpdates(genCtx); err != nil {
		return err
	}
	return nil
}

func generateGemini(ctx context.Context, session *GeminiSession, config *genai.GenerateContentConfig) (res *genai.GenerateContentResponse, err error) {
	return generateGeminiContents(ctx, session.Model, session.ToGenaiContents(), config)
}

func generateGeminiContents(ctx context.Context, model string, contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (res *genai.GenerateContentResponse, err error) {
	client := getGenAiClient()
	base := 3.0
	jitter := 0.1
	multiplier := 1.5
	maxDelay := 180.0
	jit := func() float64 {
		return 1.0 + (rand.Float64()*2-1)*jitter
	}
	// rand/v2 可安全使用全局rand
	current := base
	sleepCtx := func(seconds float64) {
		d := time.Duration(seconds * float64(time.Second))
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			return
		}
	}
	for i := range 5 {
		wait := func() {
			if i == 4 {
				return
			}
			sleepCtx(current * jit())
			current = current * multiplier
			if current > maxDelay {
				current = maxDelay
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		res, err = client.Models.GenerateContent(ctx, model, contents, config)
		if err != nil {
			wait()
			continue
		}
		if res.PromptFeedback != nil {
			return
		}
		if res.Text() == "" {
			wait()
			continue
		}
		return

	}
	return
}

func Init(bot *gotgbot.Bot, logger *slog.Logger) {
	mainBot = bot
	log = logger
}
