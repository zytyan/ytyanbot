package main

import (
	"context"
	"log/slog"
	g "main/globalcfg"
	hdrs "main/handlers"
	"main/handlers/genbot"
	"main/http/backend"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/inlinequery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"
)

var log = g.GetLogger("main", slog.LevelInfo)
var compileTime = "unknown"

type GroupedDispatcher struct {
	*ext.Dispatcher
	autoInc int
	logger  *slog.Logger
	mutex   sync.Mutex
}

type HookedHandler struct {
	ext.Handler
	logger      *slog.Logger
	hitCounter  atomic.Int64
	funcName    string
	checkerName string
}

func (h *HookedHandler) CheckUpdate(b *gotgbot.Bot, ctx *ext.Context) bool {
	start := time.Now()
	var check bool
	defer func() {
		dur := time.Since(start)
		h.logger.Debug("check update",
			"elapsed", dur,
			"name", h.Name(),
			"func_name", h.funcName,
			"check_name", h.checkerName,
			"update_id", ctx.UpdateId,
			"check", check)
	}()
	check = h.Handler.CheckUpdate(b, ctx)
	return check
}

func (h *HookedHandler) HandleUpdate(b *gotgbot.Bot, ctx *ext.Context) error {
	start := time.Now()
	newCnt := h.hitCounter.Add(1)
	defer func() {
		dur := time.Since(start)
		h.logger.Debug("handle update",
			"elapsed", dur,
			"func_name", h.funcName,
			"check_name", h.checkerName,
			"name", h.Name(),
			"update_id", ctx.UpdateId,
			"hit_count", newCnt)
	}()
	return h.Handler.HandleUpdate(b, ctx)
}

func (g *GroupedDispatcher) inc() int {
	g.mutex.Lock()
	g.autoInc++
	g.mutex.Unlock()
	return g.autoInc
}

func funcName(f any) (res string) {
	defer func() {
		if err := recover(); err != nil {
			res = ""
		}
	}()
	pc := reflect.ValueOf(f).Pointer()
	fn := runtime.FuncForPC(pc)
	return fn.Name()
}

func (g *GroupedDispatcher) Command(command string, handler handlers.Response) {
	hdr := &HookedHandler{
		Handler:     handlers.NewCommand(command, handler),
		logger:      g.logger,
		hitCounter:  atomic.Int64{},
		funcName:    funcName(handler),
		checkerName: "cmd(" + command + ")",
	}
	g.AddHandlerToGroup(hdr, g.inc())
}

func (g *GroupedDispatcher) NewMessage(msg filters.Message, handler handlers.Response) {
	hdr := &HookedHandler{
		Handler:     handlers.NewMessage(msg, handler),
		logger:      g.logger,
		hitCounter:  atomic.Int64{},
		funcName:    funcName(handler),
		checkerName: funcName(msg),
	}
	g.AddHandlerToGroup(hdr, g.inc())
}

func (g *GroupedDispatcher) NewCallback(filter filters.CallbackQuery, handler handlers.Response) {
	hdr := &HookedHandler{
		Handler:     handlers.NewCallback(filter, handler),
		logger:      g.logger,
		hitCounter:  atomic.Int64{},
		funcName:    funcName(handler),
		checkerName: funcName(filter),
	}
	g.AddHandlerToGroup(hdr, g.inc())
}

func (g *GroupedDispatcher) NewInlineQuery(callback filters.InlineQuery, handler handlers.Response) {
	hdr := &HookedHandler{
		Handler:     handlers.NewInlineQuery(callback, handler),
		logger:      g.logger,
		hitCounter:  atomic.Int64{},
		funcName:    funcName(handler),
		checkerName: funcName(callback),
	}
	g.AddHandlerToGroup(hdr, g.inc())
}

func newBot(token string) *gotgbot.Bot {
	bot := &gotgbot.Bot{
		Token: token,
		User:  gotgbot.User{},
		BotClient: gotgbot.BotClient(&gotgbot.BaseBotClient{
			Client:             http.Client{},
			UseTestEnvironment: false,
			DefaultRequestOpts: &gotgbot.RequestOpts{
				APIURL:  g.GetConfig().TgApiUrl,
				Timeout: time.Second * 130},
		}),
	}
	me, err := bot.GetMe(nil)
	if err != nil {
		panic(err)
	}
	bot.User = *me
	return bot
}

func slogLogFields(b *gotgbot.Bot, ctx *ext.Context) []any {
	fields := make([]any, 0, 16)
	fields = append(fields,
		"update_id", ctx.UpdateId,
		"bot_id", b.Id,
		"bot_username", b.Username,
	)
	if ctx.EffectiveChat != nil {
		fields = append(fields, "effective_chat_id", ctx.EffectiveChat.Id)
	}
	if ctx.EffectiveUser != nil {
		fields = append(fields, "effective_user_id", ctx.EffectiveUser.Id)
	}
	if ctx.EffectiveSender != nil {
		fields = append(fields, "effective_sender_id", ctx.EffectiveSender.Id())
	}
	if ctx.EffectiveMessage != nil {
		fields = append(fields, "effective_msg_id", ctx.EffectiveMessage.MessageId)
	}
	return fields
}

func main() {
	log.Info("compile time", "compile_time", compileTime)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		if err := g.Q.FlushChatStats(context.Background()); err != nil {
			log.Error("flush chat stats", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		log.Info("save chat stats")
		if err := g.Q.FlushChatStats(context.Background()); err != nil {
			log.Error("flush chat stats", "err", err)
		}
		os.Exit(0)
	}()
	token := g.GetConfig().BotToken
	b := newBot(token)
	hdrs.SetMainBot(b)
	hdrs.StartChatStatScheduler()
	backend.GoListenAndServe(g.GetConfig().BackendAddr, b)
	go hdrs.HttpListen4019()
	dp := GroupedDispatcher{Dispatcher: ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			fields := slogLogFields(b, ctx)
			fields = append(fields, "err", err)
			log.Warn("an error occurred while handling update", fields...)
			return ext.DispatcherActionContinueGroups
		},
		Panic: func(b *gotgbot.Bot, ctx *ext.Context, r interface{}) {
			fields := slogLogFields(b, ctx)
			fields = append(fields,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			log.Error("recovered from panic, a panic occurred while handling update.", fields...)
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	}),
		autoInc: 0,
		logger:  g.GetLogger("handler-midware", slog.LevelWarn),
		mutex:   sync.Mutex{}}
	updater := ext.NewUpdater(dp.Dispatcher, &ext.UpdaterOpts{
		UnhandledErrFunc: func(err error) {
			log.Error("an error occurred while handling update", "err", err)
		},
	},
	)
	genBotLogger := g.GetLogger("genbot", slog.LevelInfo)
	genbot.Init(b, genBotLogger)
	dp.NewMessage(message.All, hdrs.StatMessage)
	dp.NewInlineQuery(inlinequery.All, hdrs.BiliMsgConverterInline)

	dp.Command("roll", hdrs.Roll)
	dp.Command("ocr", hdrs.OcrMessage)
	dp.Command("score", hdrs.CmdScore)
	dp.Command("prpr", hdrs.GenPrpr)
	dp.Command("calc", hdrs.SolveMath)
	dp.Command("downloadvideo", hdrs.DownloadVideo)
	dp.Command("downloadaudio", hdrs.DownloadAudio)
	dp.Command("getrank", hdrs.GetRank)
	dp.Command("diag_sendstat", hdrs.SendGroupStat)
	dp.Command("cochelp", hdrs.CoCHelp)
	dp.Command("list_attr", hdrs.ListDndAttr)
	dp.Command("del_attr", hdrs.DelDndAttr)
	dp.Command("new_battle", hdrs.NewBattle)
	dp.Command("webp2png", hdrs.WebpToPng)
	dp.Command("chat_config", hdrs.ShowChatCfg)

	dp.Command("sysprompt", genbot.UpdateGeminiSysPrompt)
	dp.Command("reset_sysprompt", genbot.ResetGeminiSysPrompt)
	dp.Command("get_sysprompt", genbot.GetGeminiSysPrompt)
	dp.Command("new_session", genbot.NewGeminiSession)
	dp.Command("session_id", genbot.GetGeminiSessionId)
	dp.Command("session_help", genbot.SessionHelp)
	dp.Command("change_model", genbot.ChangeGeminiModel)
	dp.Command("model", genbot.ChangeGeminiModel)
	dp.Command("show_usage", genbot.ToggleShowUsage)
	dp.NewMessage(hdrs.BiliMsgFilter, hdrs.BiliMsgConverter)
	dp.NewMessage(hdrs.DetectNsfwPhoto, hdrs.NsfwDetect)
	dp.NewMessage(hdrs.NeedSolve, hdrs.SolveMath)
	dp.NewMessage(hdrs.IsCalcExchangeRate, hdrs.ExchangeRateCalc)
	dp.NewMessage(hdrs.IsBilibiliInlineBtn2, hdrs.SaveBiliMsgCallbackMsgId)
	dp.NewMessage(hdrs.IsDndDice, hdrs.DndDice)
	dp.NewMessage(hdrs.IsSetDndAttr, hdrs.SetDndAttr)
	dp.NewMessage(hdrs.RequireNsfw, hdrs.SendRandRacy)
	dp.NewMessage(hdrs.IsSacabam, hdrs.GenSacabam)
	dp.NewMessage(hdrs.IsBattleCommand, hdrs.ExecuteBattleCommand)
	dp.NewMessage(genbot.IsGeminiReq, genbot.GeminiReply)

	dp.NewCallback(hdrs.IsStopBattle, hdrs.StopBattle)
	dp.NewCallback(hdrs.IsNextRound, hdrs.NextRound)
	dp.NewCallback(hdrs.IsBilibiliBtn, hdrs.DownloadVideoCallback)
	dp.NewCallback(hdrs.IsBilibiliInlineBtn, hdrs.DownloadInlinedBv)
	dp.NewCallback(hdrs.IsNsfwPicRateBtn, hdrs.RateNsfwPicByBtn)
	dp.NewCallback(hdrs.IsDelMsgCallback, hdrs.DelMessage)
	dp.NewCallback(callbackquery.Prefix(hdrs.GroupConfigModifyPrefix), hdrs.ModifyGroupConfigByButton)
	dp.NewCallback(genbot.IsModelCallback, genbot.ChangeModelByButton)
	dp.NewCallback(genbot.IsUsageCallback, genbot.ShowUsageByButton)

	err := updater.StartPolling(b, &ext.PollingOpts{
		DropPendingUpdates:    g.GetConfig().DropPendingUpdates,
		EnableWebhookDeletion: false,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: 120,
		},
	})
	if err != nil {
		panic("failed to start polling: " + err.Error())
	}
	log.Info("bot started", "username", b.Username)
	updater.Idle()
}
