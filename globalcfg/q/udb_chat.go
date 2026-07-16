package q

import (
	"context"
	"main/helpers/lrusf"
	"strconv"
	"time"
)

type ChatCfg struct {
	ID             int64 `json:"id"`
	AutoCvtBili    bool  `json:"auto_cvt_bili"  btnTxt:"自动转换Bilibili视频链接" pos:"1,1"`
	AutoCalculate  bool  `json:"auto_calculate" btnTxt:"自动计算算式" pos:"2,1"`
	AutoExchange   bool  `json:"auto_exchange"  btnTxt:"自动换算汇率" pos:"2,2"`
	AutoCheckAdult bool  `json:"auto_check_adult"`
	EnableCoc      bool  `json:"enable_coc"     btnTxt:"启用CoC辅助" pos:"3,1"`
	RespNsfwMsg    bool  `json:"resp_nsfw_msg"  btnTxt:"响应来张色图" pos:"3,2"`
	Timezone       int64 `json:"timezone"`
	InDatabase     bool  `json:"in_database"`
}

func fromInnerCfg(cfg *chatCfg) *ChatCfg {
	return &ChatCfg{
		ID:             cfg.ID,
		AutoCvtBili:    cfg.AutoCvtBili,
		AutoCalculate:  cfg.AutoCalculate,
		AutoExchange:   cfg.AutoExchange,
		AutoCheckAdult: cfg.AutoCheckAdult,
		EnableCoc:      cfg.EnableCoc,
		RespNsfwMsg:    cfg.RespNsfwMsg,
		Timezone:       cfg.Timezone,
		InDatabase:     true,
	}
}

func defaultChagCfg(id int64) *ChatCfg {
	return &ChatCfg{
		ID:             id,
		AutoCvtBili:    false,
		AutoCalculate:  false,
		AutoExchange:   false,
		AutoCheckAdult: false,
		EnableCoc:      false,
		RespNsfwMsg:    false,
		Timezone:       8 * 60 * 60,
		InDatabase:     false,
	}
}

func (c *ChatCfg) Save(ctx context.Context, q *Queries) error {
	if !c.InDatabase {
		c.InDatabase = true
		return q.CreateChatCfg(ctx, CreateChatCfgParams{
			ID:             c.ID,
			AutoCvtBili:    c.AutoCvtBili,
			AutoCalculate:  c.AutoCalculate,
			AutoExchange:   c.AutoExchange,
			AutoCheckAdult: c.AutoCheckAdult,
			EnableCoc:      c.EnableCoc,
			RespNsfwMsg:    c.RespNsfwMsg,
			Timezone:       c.Timezone,
		})
	}
	return q.updateChatCfg(ctx, updateChatCfgParams{
		AutoCvtBili:    c.AutoCvtBili,
		AutoCalculate:  c.AutoCalculate,
		AutoExchange:   c.AutoExchange,
		AutoCheckAdult: c.AutoCheckAdult,
		EnableCoc:      c.EnableCoc,
		RespNsfwMsg:    c.RespNsfwMsg,
		ID:             c.ID,
	})
}

func id2str(id int64) string {
	return strconv.FormatInt(id, 16)
}

var chatCache *lrusf.Cache[int64, *ChatCfg]

func (q *Queries) GetChatCfgById(ctx context.Context, id int64) (*ChatCfg, error) {
	return chatCache.Get(id, func() (*ChatCfg, error) {
		cfg, err := q.getChatCfgById(ctx, id)
		return fromInnerCfg(&cfg), err
	})
}

func (q *Queries) GetChatCfgByIdOrDefault(id int64) *ChatCfg {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	cfg, err := q.GetChatCfgById(ctx, id)
	if err != nil {
		return defaultChagCfg(id)
	}
	return cfg
}
