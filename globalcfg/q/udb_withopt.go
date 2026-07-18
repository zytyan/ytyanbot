package q

import (
	"context"
	"log/slog"
	"main/helpers/lrusf"
	"strconv"
	"sync"
	"time"
)

func PrepareWithLogger(ctx context.Context, db DBTX, _ *slog.Logger) (*Queries, error) {
	query, err := Prepare(ctx, db)
	if err != nil {
		return nil, err
	}
	InitCache(query)
	return query, nil
}

func InitCache(q *Queries) { initCaches(q) }

var cacheOnce sync.Once

func chatStatCacheKey(key ChatStatKey) string {
	buf := make([]byte, 0, 17)
	buf = strconv.AppendInt(buf, key.Id, 16)
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, key.Day, 16)
	return string(buf)
}

func initCaches(q *Queries) {
	cacheOnce.Do(func() {
		userCache = lrusf.NewCache[int64, *User](2048, id2str, nil)
		chatCache = lrusf.NewCache[int64, *ChatCfg](2048, id2str, func(i int64, cfg *ChatCfg) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
			defer cancel()
			if err := cfg.Save(ctx, q); err != nil {
				slog.Error("save evicted chat config", "chat_id", i, "err", err)
			}
		})
		chatStatCache = lrusf.NewCache[ChatStatKey, *ChatStat](64, chatStatCacheKey, func(key ChatStatKey, daily *ChatStat) {
			saveEvictedChatStat(q, key, daily)
		})
	})
}
