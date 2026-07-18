package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"main/globalcfg"
	"main/helpers/cocdice"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/google/uuid"
	"golang.org/x/text/width"
)

var dndDiceRe = regexp.MustCompile(`^((\d+)\s*)?([dDbBpP])\s*(\d+)?(\s*([+-])(\d+))?\s*(/?\s*([\w\p{Han}\p{Hiragana}\p{Katakana}]+))?$`)
var setAttrRe = regexp.MustCompile(`^([a-zA-Z\p{Han}\p{Hiragana}\p{Katakana}][a-zA-Z0-9\p{Han}\p{Hiragana}\p{Katakana}]{0,20})\s*(\+=?|-=?|=)\s*(\d+)$`)

type CharacterAttr struct {
	ID        int `gorm:"primaryKey"`
	UserID    int64
	AttrName  string
	AttrValue string
}

func defaultAtoi(s string, d int) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return i
}

func IsDndDice(msg *gotgbot.Message) bool {
	if msg.Chat.Type != "private" && !chatCfg(msg.Chat.Id).EnableCoc {
		return false
	}
	text := width.Narrow.String(msg.Text)
	return dndDiceRe.MatchString(text)
}

func IsSetDndAttr(msg *gotgbot.Message) bool {
	if msg.Chat.Type != "private" && !chatCfg(msg.Chat.Id).EnableCoc {
		return false
	}
	text := width.Narrow.String(msg.Text)
	text = strings.SplitN(text, "\n", 2)[0]
	return setAttrRe.MatchString(text)
}
func getUserName(user *gotgbot.User) string {
	if user == nil {
		return "未知"
	}
	if user.LastName == "" {
		return user.FirstName
	}
	return user.FirstName + " " + user.LastName
}
func getAttrTarget(ctx *ext.Context) (int64, string) {
	if ctx.EffectiveMessage.ReplyToMessage != nil && ctx.EffectiveMessage.ReplyToMessage.From != nil {
		from := ctx.EffectiveMessage.ReplyToMessage.From
		return from.Id, getUserName(from)
	}
	return ctx.EffectiveSender.Id(), ctx.EffectiveSender.Name()
}
func SetDndAttr(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	text := width.Narrow.String(ctx.EffectiveMessage.Text)
	lines := strings.Fields(text)
	buf := strings.Builder{}
	userId, username := getAttrTarget(ctx)
	buf.WriteString(fmt.Sprintf("用户：%s\n", username))
	modified := false
	for _, line := range lines {
		matches := setAttrRe.FindStringSubmatch(line)
		name := matches[1]
		val, err1 := g.Q.GetCocCharAttr(context.Background(), userId, name)
		if val == "" || errors.Is(err1, sql.ErrNoRows) {
			val = "empty"
		} else if err1 != nil {
			continue
		}
		oldVal := val

		switch matches[2] {
		case "+", "+=":
			val = strconv.Itoa(defaultAtoi(val, 0) + defaultAtoi(matches[3], 0))
		case "-", "-=":
			val = strconv.Itoa(defaultAtoi(val, 0) - defaultAtoi(matches[3], 0))
		default:
			val = matches[3]
		}
		modified = true
		buf.WriteString(fmt.Sprintf("%s : %s -> %s\n", name, oldVal, val))
		err = g.Q.SetCocCharAttr(context.Background(), userId, name, val)
		if err != nil {
			log.Error("set coc char attr error", "err", err)
		}
	}
	if !modified {
		return nil
	}
	s := buf.String()
	s = s[:len(s)-1]
	_, err = ctx.EffectiveMessage.Reply(bot, "设置成功\n"+s, nil)
	return err
}

func getAbility(m string, user int64) (int, error) {
	ability, err := strconv.Atoi(m)
	if err != nil {
		val, err := g.Q.GetCocCharAttr(context.Background(), user, m)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(val)

	}
	return ability, nil
}

func ListDndAttr(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	text := width.Narrow.String(ctx.EffectiveMessage.Text)
	texts := strings.SplitN(text, " ", 2)
	var re *regexp.Regexp
	if len(texts) > 1 {
		text = texts[1]
		text = strings.TrimSpace(text)
		re, _ = regexp.Compile(text)
	}
	log.Info("list dnd attr", "text", text)
	userId, name := getAttrTarget(ctx)
	attrs, err := g.Q.GetCocCharAllAttr(context.Background(), userId)
	if err != nil {
		return err
	}
	var result strings.Builder
	result.WriteString(fmt.Sprintf("用户：%s\n", name))
	for _, attr := range attrs {
		if re == nil || re.FindString(attr.AttrName) != "" {
			result.WriteString(attr.AttrName)
			result.WriteString(" = ")
			result.WriteString(attr.AttrValue)
			result.WriteString("\n")
		}
	}
	if result.Len() == 0 {
		result.WriteString(fmt.Sprintf("没有找到属性：%s", text))
	}
	_, err = ctx.EffectiveMessage.Reply(bot, result.String(), nil)
	return err
}

func DelDndAttr(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	text := width.Narrow.String(ctx.EffectiveMessage.Text)
	lines := strings.Split(text, "\n")
	for _, line := range lines[1:] {
		err = g.Q.DelCocCharAttr(context.Background(), ctx.EffectiveSender.Id(), line)
		if err != nil {
			log.Error("del coc char attr error", "err", err)
		}
	}
	_, err = ctx.EffectiveMessage.Reply(bot, "删除成功", nil)
	return err
}

func DndDice(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	text := width.Narrow.String(ctx.EffectiveMessage.Text)
	matches := dndDiceRe.FindStringSubmatch(text)
	diceCount := defaultAtoi(matches[2], 1)
	diceType := strings.ToLower(matches[3])
	diceFace := defaultAtoi(matches[4], 100)
	modifier, _ := strconv.Atoi(matches[7])
	var ability int
	if matches[9] != "" {
		ability, err = getAbility(matches[9], ctx.EffectiveSender.Id())
		if err != nil {
			log.Info("get ability error", "err", err)
			return nil
		}
	}
	diceCommand := cocdice.DiceCommand{Arg1: diceCount, Arg2: diceFace, Modifier: modifier, Ability: ability}
	switch diceType {
	case "d":
		diceCommand.Type = cocdice.NormalDice
	case "b":
		diceCommand.Type = cocdice.BonusDice
	case "p":
		diceCommand.Type = cocdice.PenaltyDice
	}
	result := diceCommand.Roll()
	_, err = ctx.EffectiveMessage.Reply(bot, result, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func CoCHelp(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	helpText := `<b>在使用CoC辅助之前，请先使用 /config_chat 命令启用CoC功能</b>

<b>骰子功能</b>
<b>用法：</b>
1. [骰子数量]d[骰子面数] [+/-调整值]
2. [骰子数量]b[骰子面数] [+/-调整值]
3. [骰子数量]p[骰子面数] [+/-调整值]
<b>示例：</b>
1. 3d6 投掷3个6面骰子
2. 2b100 投掷2个100面奖励骰
3. 1p100 投掷1个100面惩罚骰
4. 1d20+5 投掷1个20面骰子并加5
`
	_, err = ctx.EffectiveMessage.Reply(bot, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

type battleSession struct {
	mu      sync.Mutex
	groupID int64
	round   *cocdice.BattleRound
}

func (s *battleSession) render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.round.String()
}

func (s *battleSession) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.round.NextCharacter()
	return s.round.String()
}

func (s *battleSession) execute(commands []string) (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	errors := make([]string, 0)
	for _, command := range commands {
		if err := s.round.ParseCommand(command); err != nil {
			errors = append(errors, err.Error())
		}
	}
	return s.round.String(), errors
}

type battleRegistry struct {
	mu      sync.RWMutex
	byID    map[string]*battleSession
	byGroup map[int64]string
}

func newBattleRegistry() *battleRegistry {
	return &battleRegistry{
		byID:    make(map[string]*battleSession),
		byGroup: make(map[int64]string),
	}
}

func (r *battleRegistry) add(uid string, session *battleSession) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[uid]; exists {
		return false
	}
	if _, exists := r.byGroup[session.groupID]; exists {
		return false
	}
	r.byGroup[session.groupID] = uid
	r.byID[uid] = session
	return true
}

func (r *battleRegistry) byUID(uid string, groupID int64) (*battleSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.byID[uid]
	return session, ok && session.groupID == groupID
}

func (r *battleRegistry) forGroup(groupID int64) (string, *battleSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	uid, ok := r.byGroup[groupID]
	if !ok {
		return "", nil, false
	}
	session, ok := r.byID[uid]
	return uid, session, ok
}

func (r *battleRegistry) remove(uid string, groupID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.byID[uid]
	if !ok || session.groupID != groupID {
		return false
	}
	delete(r.byID, uid)
	if r.byGroup[groupID] == uid {
		delete(r.byGroup, groupID)
	}
	return true
}

func (r *battleRegistry) hasGroup(groupID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byGroup[groupID]
	return ok
}

var battles = newBattleRegistry()

func buildBattleKeyboard(uid string) gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{gotgbot.InlineKeyboardButton{CallbackData: "battle:next:" + uid, Text: "下一回合"}},
			{gotgbot.InlineKeyboardButton{CallbackData: "battle:stop:" + uid, Text: "结束战斗"}},
		},
	}
}

func NewBattle(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	gid := ctx.EffectiveChat.Id
	log.Info("battle gid", "gid", gid)
	uid, _ := uuid.NewUUID()
	uidStr := uid.String()
	session := &battleSession{groupID: gid, round: cocdice.NewFromText(ctx.EffectiveMessage.Text)}
	if !battles.add(uidStr, session) {
		_, err = ctx.EffectiveMessage.Reply(bot, "已经有一个战斗在进行中", nil)
		return err
	}
	_, err = ctx.EffectiveMessage.Reply(bot, session.render(), &gotgbot.SendMessageOpts{ParseMode: "HTML",
		ReplyMarkup: buildBattleKeyboard(uidStr),
	})
	return err
}

func IsNextRound(msg *gotgbot.CallbackQuery) bool {
	return strings.HasPrefix(msg.Data, "battle:next:")
}

func IsStopBattle(msg *gotgbot.CallbackQuery) bool {
	return strings.HasPrefix(msg.Data, "battle:stop:")
}

func StopBattle(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	uid := getBattleUid(ctx.CallbackQuery.Data)
	battles.remove(uid, ctx.EffectiveChat.Id)
	_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "战斗结束", ShowAlert: true})
	_, err = ctx.EffectiveMessage.Reply(bot, "战斗结束", nil)
	return err
}

func NextRound(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	uid := getBattleUid(ctx.CallbackQuery.Data)
	battle, ok := battles.byUID(uid, ctx.EffectiveChat.Id)
	if !ok {
		_, err = ctx.EffectiveMessage.Reply(bot, "战斗已经结束", nil)
		return err
	}
	result := battle.next()
	_, _ = ctx.CallbackQuery.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: "下一回合", ShowAlert: false})
	_, err = ctx.EffectiveMessage.Reply(bot, result, &gotgbot.SendMessageOpts{ParseMode: "HTML",
		ReplyMarkup: buildBattleKeyboard(uid),
	})
	return err
}

var reBattleCmd = regexp.MustCompile(`^(add|chg|del|stat)`)

func getBattleUid(data string) string {
	return strings.Split(data, ":")[2]
}

func IsBattleCommand(msg *gotgbot.Message) bool {
	gid := msg.Chat.Id
	if battles.hasGroup(gid) {
		return reBattleCmd.MatchString(msg.Text)
	}
	if msg.ReplyToMessage == nil {
		return false
	}
	replyToMsg := msg.ReplyToMessage
	if replyToMsg.ReplyMarkup == nil || len(replyToMsg.ReplyMarkup.InlineKeyboard) == 0 || len(replyToMsg.ReplyMarkup.InlineKeyboard[0]) == 0 {
		return false
	}
	if !strings.HasPrefix(replyToMsg.ReplyMarkup.InlineKeyboard[0][0].CallbackData, "battle:") {
		return false
	}
	log.Info("battle command", "text", msg.Text)
	return reBattleCmd.MatchString(msg.Text)
}

func ExecuteBattleCommand(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	uid, battle, ok := battles.forGroup(ctx.EffectiveChat.Id)
	if !ok {
		_, err = ctx.EffectiveMessage.Reply(bot, "战斗已经结束", nil)
		return err
	}
	textList := strings.Split(ctx.EffectiveMessage.Text, "\n")
	for _, text := range textList {
		log.Info("battle command", "text", text)
	}
	result, errList := battle.execute(textList)
	if len(errList) > 0 {
		errStr := strings.Join(errList, "\n")
		_, err = ctx.EffectiveMessage.Reply(bot, errStr, nil)
		return err
	}
	_, err = ctx.EffectiveMessage.Reply(bot, result, &gotgbot.SendMessageOpts{ParseMode: "HTML",
		ReplyMarkup: buildBattleKeyboard(uid),
	})
	return err
}
