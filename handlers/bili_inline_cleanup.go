package handlers

import (
	"context"
	g "main/globalcfg"
	"main/globalcfg/q"
	"time"

	"github.com/go-co-op/gocron"
)

const biliInlineTTL = 30 * 24 * time.Hour

var biliInlineCleanupScheduler *gocron.Scheduler

func cleanupExpiredBiliInlineData() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deleted, err := g.Q.DeleteExpiredBiliInlineData(ctx, q.UnixTime{Time: time.Now().Add(-biliInlineTTL)})
	if err != nil {
		log.Warn("cleanup expired bilibili inline data", "err", err)
		return
	}
	log.Info("cleanup expired bilibili inline data", "deleted", deleted)
}

func StartBiliInlineCleanup() {
	cleanupExpiredBiliInlineData()
	biliInlineCleanupScheduler = gocron.NewScheduler(time.Local)
	if _, err := biliInlineCleanupScheduler.Every(1).Day().Do(cleanupExpiredBiliInlineData); err != nil {
		log.Warn("start bilibili inline cleanup scheduler", "err", err)
		return
	}
	biliInlineCleanupScheduler.StartAsync()
}
