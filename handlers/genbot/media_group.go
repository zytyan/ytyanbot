package genbot

import (
	"context"
	"database/sql"
	"errors"
	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/helpers/ent2md"
	"slices"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/go-co-op/gocron"
)

const (
	aiMediaGroupTTL       = 7 * 24 * time.Hour
	aiMediaGroupIdle      = time.Second
	aiMediaGroupMaxWait   = 5 * time.Second
	aiMediaGroupClaimKeep = time.Minute
)

var (
	ErrAIMediaGroupUnavailable = errors.New("图片组不存在或已超过七天，无法载入完整图片组")
	ErrAIMediaGroupMixed       = errors.New("暂不支持包含视频、文档或其他媒体的混合图片组")
	ErrAIMediaGroupDownload    = errors.New("图片组下载失败，未向 AI 发送不完整的图片组")
)

type aiMediaGroupKey struct {
	chatID       int64
	mediaGroupID string
}

type aiMediaGroupActivity struct {
	lastSeen time.Time
	claimed  bool
}

var aiMediaGroupActivities = struct {
	sync.Mutex
	items map[aiMediaGroupKey]aiMediaGroupActivity
}{items: make(map[aiMediaGroupKey]aiMediaGroupActivity)}

var aiMediaGroupCleanupScheduler *gocron.Scheduler

func IsAIMediaGroup(msg *gotgbot.Message) bool {
	return msg != nil && msg.MediaGroupId != "" && slices.Contains(g.GetConfig().AIChats, msg.Chat.Id)
}

// CaptureAIMediaGroup records only Telegram metadata. Image bytes are fetched
// later, when a user actually asks the AI to inspect the album.
func CaptureAIMediaGroup(_ *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.MediaGroupId == "" || !slices.Contains(g.GetConfig().AIChats, msg.Chat.Id) {
		return nil
	}
	sentAt := msg.Date
	photoOnly := int64(0)
	if len(msg.Photo) > 0 {
		photoOnly = 1
	}
	if err := g.AIQ.UpsertAIMediaGroup(context.Background(), aiq.UpsertAIMediaGroupParams{
		ChatID: msg.Chat.Id, MediaGroupID: msg.MediaGroupId, FirstSeenAt: sentAt,
		UpdatedAt: sentAt, ExpiresAt: sentAt + int64(aiMediaGroupTTL/time.Second), PhotoOnly: photoOnly,
	}); err != nil {
		return err
	}
	if photoOnly == 1 {
		sender := msg.GetSender()
		username := sender.Username()
		caption := ent2md.TgMsgTextToMarkdown(msg)
		photo := msg.Photo[len(msg.Photo)-1]
		if err := g.AIQ.UpsertAIMediaGroupPhoto(context.Background(), aiq.UpsertAIMediaGroupPhotoParams{
			ChatID: msg.Chat.Id, MediaGroupID: msg.MediaGroupId, MsgID: msg.MessageId,
			SentAt: sentAt, UserID: sender.Id(), Username: sender.Name(),
			AtableUsername: sql.NullString{String: username, Valid: username != ""},
			Caption:        sql.NullString{String: caption, Valid: caption != ""}, TelegramFileID: photo.FileId,
		}); err != nil {
			return err
		}
	}
	noteAIMediaGroupActivity(aiMediaGroupKey{chatID: msg.Chat.Id, mediaGroupID: msg.MediaGroupId})
	return nil
}

func noteAIMediaGroupActivity(key aiMediaGroupKey) {
	aiMediaGroupActivities.Lock()
	activity := aiMediaGroupActivities.items[key]
	activity.lastSeen = time.Now()
	aiMediaGroupActivities.items[key] = activity
	aiMediaGroupActivities.Unlock()
}

func claimAIMediaGroup(key aiMediaGroupKey) bool {
	aiMediaGroupActivities.Lock()
	activity := aiMediaGroupActivities.items[key]
	if activity.claimed {
		aiMediaGroupActivities.Unlock()
		return false
	}
	activity.claimed = true
	if activity.lastSeen.IsZero() {
		activity.lastSeen = time.Now()
	}
	aiMediaGroupActivities.items[key] = activity
	aiMediaGroupActivities.Unlock()
	time.AfterFunc(aiMediaGroupClaimKeep, func() {
		aiMediaGroupActivities.Lock()
		delete(aiMediaGroupActivities.items, key)
		aiMediaGroupActivities.Unlock()
	})
	return true
}

func waitForAIMediaGroup(ctx context.Context, key aiMediaGroupKey) error {
	deadline := time.Now().Add(aiMediaGroupMaxWait)
	for {
		aiMediaGroupActivities.Lock()
		lastSeen := aiMediaGroupActivities.items[key].lastSeen
		aiMediaGroupActivities.Unlock()
		readyAt := lastSeen.Add(aiMediaGroupIdle)
		if now := time.Now(); !now.Before(readyAt) || !now.Before(deadline) {
			return nil
		}
		wait := time.Until(readyAt)
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func validateAIMediaGroup(group aiq.AiMediaGroup, now time.Time) error {
	if group.ExpiresAt <= now.Unix() {
		return ErrAIMediaGroupUnavailable
	}
	if group.PhotoOnly == 0 {
		return ErrAIMediaGroupMixed
	}
	return nil
}

func cleanupExpiredAIMediaGroups() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	deleted, err := g.AIQ.DeleteExpiredAIMediaGroups(ctx, time.Now().Unix())
	if err != nil {
		log.Warn("cleanup expired AI media groups", "err", err)
		return
	}
	log.Info("cleanup expired AI media groups", "deleted", deleted)
}

func StartAIMediaGroupCleanup() {
	cleanupExpiredAIMediaGroups()
	aiMediaGroupCleanupScheduler = gocron.NewScheduler(time.Local)
	if _, err := aiMediaGroupCleanupScheduler.Every(1).Day().Do(cleanupExpiredAIMediaGroups); err != nil {
		log.Warn("start AI media group cleanup scheduler", "err", err)
		return
	}
	aiMediaGroupCleanupScheduler.StartAsync()
}
