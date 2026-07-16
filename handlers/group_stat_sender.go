package handlers

import (
	"context"
	"fmt"
	"html"
	g "main/globalcfg"
	"main/globalcfg/q"
	"sort"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/go-co-op/gocron"
)

var (
	statScheduler *gocron.Scheduler
	statJob       *gocron.Job
)

func mostActiveUsers(stat *q.ChatStatSnapshot) (users []int64, counts []int64) {
	if stat == nil || len(stat.UserMsgStat) == 0 {
		return
	}
	type userCount struct {
		user int64
		stat *q.UserMsgStat
	}
	tmp := make([]userCount, 0, len(stat.UserMsgStat))
	for u, c := range stat.UserMsgStat {
		if c == nil {
			continue
		}
		tmp = append(tmp, userCount{u, c})
	}
	sort.Slice(tmp, func(i, j int) bool {
		return tmp[i].stat.MsgCount > tmp[j].stat.MsgCount
	})
	for i := 0; i < 3 && i < len(tmp); i++ {
		users = append(users, tmp[i].user)
		counts = append(counts, tmp[i].stat.MsgCount)
	}
	return
}

func mostActiveTimeSeg(stat *q.ChatStatSnapshot) (timeId int, timeSeg string, count int64) {
	if stat == nil {
		return
	}
	for i, c := range stat.MsgCountByTime {
		if c > count {
			count = c
			timeId = i
		}
	}
	hour := timeId / 6
	minute := (timeId % 6) * 10
	timeSeg = fmt.Sprintf("%02d:%02d", hour, minute)
	return
}

func statDateString(timezone int64) string {
	loc := time.FixedZone("chat_tz", int(timezone))
	return time.Now().In(loc).Format("2006年01月02日")
}

func formatStatMessage(chatId int64, stat *q.ChatStatSnapshot, timezone int64) string {
	if stat == nil {
		return "没有数据"
	}
	today := statDateString(timezone)
	actUser, count := mostActiveUsers(stat)
	timeId, actTime, actTimeCnt := mostActiveTimeSeg(stat)
	act3Users := make([]string, 0, 3)
	mostActUser := ""
	act3UsersName := ""
	var actMaxCnt int64
	if len(actUser) > 0 {
		actMaxCnt = count[0]
	}
	for i := 0; i < 3 && i < len(actUser); i++ {
		user, err := g.Q.GetUserById(context.Background(), actUser[i])
		if err != nil {
			act3Users = append(act3Users, fmt.Sprintf("用户%d", actUser[i]))
			continue
		}
		act3Users = append(act3Users, user.Name())
	}
	if len(act3Users) == 0 {
		act3UsersName = "没有人"
		mostActUser = "没有人"
	} else if len(act3Users) == 1 {
		act3UsersName = act3Users[0]
	} else {
		act3UsersName = strings.Join(act3Users[:len(act3Users)-1], "、") + "和" + act3Users[len(act3Users)-1]
	}
	if actMaxCnt > 0 {
		mostActUser = act3Users[0]
	}
	// 转换HTML，避免被tg解析
	act3UsersName = html.EscapeString(act3UsersName)
	mostActUser = html.EscapeString(mostActUser)

	link := actTime
	msgId := stat.MsgIDAtTimeStart[timeId]
	if msgId != 0 {
		groupLinkId := -chatId - 1000000000000
		link = fmt.Sprintf(`<a href="https://t.me/c/%d/%d">%s</a>`, groupLinkId, msgId, actTime)
	}
	tmp := fmt.Sprintf(`早上好！吹水群！
今天是%s，昨天的发言统计，最后的结果是满打满算的整整%d条，你们这些家伙都不用上班的吗？
多亏了%s没完没了的摸鱼吹水，光%s一个人就发了%d条。但有一个晶哥也发话了，我看你们全都得喝茶，因为平子肯定咽不下这口气。
群里一共发了%d张图片，还有%d个表情，又是只发表情的社恐干的好事。与此同时，毅力号还在火星上替火星人找到了%d张图，火星次数最多的图让你们火星了%d次，真是一群火星人。
群里发了%d张色图，里面还有%d张R18，你们今天发色图，明天FBI就来敲你家门了。
智乃酱帮你们下了%d个视频，%d个音频，今日份的娱乐就到这里吧。
而群里最热闹的%s，这十分钟居然发了%d条，好吧，吹水群还是那个吹水群。
我是你们的铁哥们智乃酱，和我一起开启完蛋操的新一天吧！`,
		today, stat.MessageCount,
		act3UsersName, mostActUser, actMaxCnt,
		stat.PhotoCount, stat.StickerCount, stat.MarsCount, stat.MaxMarsCount,
		stat.AdultCount+stat.RacyCount, stat.AdultCount,
		stat.DownloadVideoCount, stat.DownloadAudioCount,
		link, actTimeCnt)
	return tmp
}

func sendChatStat(bot *gotgbot.Bot, chatId int64, target time.Time) error {
	if bot == nil {
		return fmt.Errorf("main bot is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Flush today's in-memory stat before reading
	if stat := g.Q.ChatStatAt(chatId, time.Now().Unix()); stat != nil {
		_ = stat.Save(ctx, g.Q)
	}
	stat, timezone, err := g.Q.ChatStatOfDay(ctx, chatId, target.Unix())
	if err != nil {
		return err
	}
	text := formatStatMessage(chatId, &stat, timezone)
	_, err = bot.SendMessage(chatId, text, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func sendMyChatsStat() {
	bot := GetMainBot()
	for _, chatId := range g.GetConfig().MyChats {
		if err := sendChatStat(bot, chatId, time.Now().Add(-24*time.Hour)); err != nil {
			log.Warn("send stat of yesterday failed", "chat_id", chatId, "err", err)
		}
	}
}

func StartChatStatScheduler() {
	cfg := g.GetConfig()
	if cfg == nil || len(cfg.MyChats) == 0 {
		return
	}
	statScheduler = gocron.NewScheduler(time.Local)
	var err error
	statJob, err = statScheduler.Every(1).Day().At("08:00").Do(sendMyChatsStat)
	if err != nil {
		log.Warn("start chat stat scheduler failed", "err", err)
		return
	}
	statScheduler.StartAsync()
}
