package q

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"main/helpers/lrusf"
	"sync"
	"time"
)

const (
	daySeconds int64 = 24 * 60 * 60
	tenMinutes       = 10 * 60
)

type UserMsgStat struct {
	MsgCount int64
	MsgLen   int64
}

type UserMsgStatMap map[int64]*UserMsgStat

type TenMinuteStats [24 * 6]int64

type ChatStatSnapshot struct {
	ChatStatDaily
	UserMsgStat      UserMsgStatMap
	MsgCountByTime   TenMinuteStats
	MsgIDAtTimeStart TenMinuteStats
}

type ChatStat struct {
	mu       sync.Mutex
	timezone int64
	ChatStatSnapshot
}

func cloneChatStatSnapshot(src ChatStatSnapshot) ChatStatSnapshot {
	dst := src
	dst.UserMsgStat = make(UserMsgStatMap, len(src.UserMsgStat))
	for userID, stat := range src.UserMsgStat {
		if stat == nil {
			dst.UserMsgStat[userID] = nil
			continue
		}
		copy := *stat
		dst.UserMsgStat[userID] = &copy
	}
	return dst
}

func (s *ChatStat) Snapshot() ChatStatSnapshot {
	if s == nil {
		return ChatStatSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneChatStatSnapshot(s.ChatStatSnapshot)
}

func (s *ChatStat) IncMessage(userId, txtLen, unixTime, messageId int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.MessageCount++
	if s.UserMsgStat == nil {
		s.UserMsgStat = make(UserMsgStatMap, 4)
	}
	stat, ok := s.UserMsgStat[userId]
	if !ok || stat == nil {
		stat = &UserMsgStat{}
		s.UserMsgStat[userId] = stat
	}
	timeSec := (unixTime + s.timezone) % daySeconds
	if timeSec < 0 {
		timeSec += daySeconds
	}
	idx := int(timeSec / tenMinutes)
	s.MsgCountByTime[idx]++
	if s.MsgIDAtTimeStart[idx] == 0 {
		s.MsgIDAtTimeStart[idx] = messageId
	}
	stat.MsgCount++
	stat.MsgLen += txtLen
	s.mu.Unlock()
}

func (s *ChatStat) IncPhotoCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.PhotoCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncVideoCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.VideoCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncStickerCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.StickerCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncForwardCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ForwardCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncMarsCount(maxMarsCount int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.MarsCount++
	s.MaxMarsCount = max(maxMarsCount, s.MaxMarsCount)
	s.mu.Unlock()
}
func (s *ChatStat) IncRacyCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.RacyCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncAdultCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.AdultCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncDownloadVideoCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.DownloadVideoCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncDownloadAudioCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.DownloadAudioCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncDioAddUserCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.DioAddUserCount++
	s.mu.Unlock()
}
func (s *ChatStat) IncDioBanUserCount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.DioBanUserCount++
	s.mu.Unlock()
}

func (s *ChatStat) Save(ctx context.Context, q *Queries) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	database, ok := q.db.(*sql.DB)
	if !ok {
		return fmt.Errorf("save chat stat requires *sql.DB, got %T", q.db)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	txq := q.WithTx(tx)
	if err = txq.UpdateChatStatDaily(ctx, UpdateChatStatDailyParams{
		MessageCount:       s.MessageCount,
		PhotoCount:         s.PhotoCount,
		VideoCount:         s.VideoCount,
		StickerCount:       s.StickerCount,
		ForwardCount:       s.ForwardCount,
		MarsCount:          s.MarsCount,
		MaxMarsCount:       s.MaxMarsCount,
		RacyCount:          s.RacyCount,
		AdultCount:         s.AdultCount,
		DownloadVideoCount: s.DownloadVideoCount,
		DownloadAudioCount: s.DownloadAudioCount,
		DioAddUserCount:    s.DioAddUserCount,
		DioBanUserCount:    s.DioBanUserCount,
		ChatID:             s.ChatID,
		StatDate:           s.StatDate,
	}); err != nil {
		return err
	}
	for userID, stat := range s.UserMsgStat {
		if stat == nil {
			return fmt.Errorf("nil chat stat for user %d", userID)
		}
		if err = txq.UpsertChatStatUser(ctx, UpsertChatStatUserParams{
			ChatID:        s.ChatID,
			StatDate:      s.StatDate,
			UserID:        userID,
			MessageCount:  stat.MsgCount,
			MessageLength: stat.MsgLen,
		}); err != nil {
			return err
		}
	}
	for bucket := range s.MsgCountByTime {
		if err = txq.UpsertChatStatBucket(ctx, UpsertChatStatBucketParams{
			ChatID:       s.ChatID,
			StatDate:     s.StatDate,
			Bucket:       int64(bucket),
			MessageCount: s.MsgCountByTime[bucket],
			FirstMsgID:   s.MsgIDAtTimeStart[bucket],
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type ChatStatKey struct {
	Day int64
	Id  int64
}

var chatStatCache *lrusf.Cache[ChatStatKey, *ChatStat]

// FlushChatStats saves all in-memory chat statistics to the database.
// It is safe to call concurrently with other stat operations.
func (q *Queries) FlushChatStats(ctx context.Context) error {
	var firstErr error
	for _, stat := range chatStatCache.Range() {
		if stat == nil {
			continue
		}
		if err := stat.Save(ctx, q); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (q *Queries) getOrCreateChatStat(ctx context.Context, chatId int64, day int64) (ChatStatSnapshot, error) {
	daily, err := q.getChatStat(ctx, chatId, day)
	if errors.Is(err, sql.ErrNoRows) {
		daily, err = q.createChatStatDaily(ctx, chatId, day)
	}
	if err != nil {
		return ChatStatSnapshot{}, err
	}
	snapshot := ChatStatSnapshot{
		ChatStatDaily: daily,
		UserMsgStat:   make(UserMsgStatMap),
	}
	users, err := q.ListChatStatUsers(ctx, chatId, day)
	if err != nil {
		return ChatStatSnapshot{}, err
	}
	for _, user := range users {
		snapshot.UserMsgStat[user.UserID] = &UserMsgStat{
			MsgCount: user.MessageCount,
			MsgLen:   user.MessageLength,
		}
	}
	buckets, err := q.ListChatStatBuckets(ctx, chatId, day)
	if err != nil {
		return ChatStatSnapshot{}, err
	}
	if len(buckets) != 0 && len(buckets) != len(snapshot.MsgCountByTime) {
		return ChatStatSnapshot{}, fmt.Errorf("chat %d day %d has %d statistic buckets", chatId, day, len(buckets))
	}
	for expected, bucket := range buckets {
		if bucket.Bucket != int64(expected) || bucket.Bucket < 0 || bucket.Bucket >= int64(len(snapshot.MsgCountByTime)) {
			return ChatStatSnapshot{}, fmt.Errorf("chat %d day %d has invalid statistic bucket %d", chatId, day, bucket.Bucket)
		}
		snapshot.MsgCountByTime[bucket.Bucket] = bucket.MessageCount
		snapshot.MsgIDAtTimeStart[bucket.Bucket] = bucket.FirstMsgID
	}
	return snapshot, nil
}

func (q *Queries) chatStatAtWithTimezone(ctx context.Context, chatId, unixTime, timezone int64) (*ChatStat, error) {
	day := (unixTime + timezone) / daySeconds
	key := ChatStatKey{
		Day: day,
		Id:  chatId,
	}
	return chatStatCache.Get(key, func() (*ChatStat, error) {
		snapshot, err := q.getOrCreateChatStat(ctx, chatId, day)
		if err != nil {
			return nil, err
		}
		stat := &ChatStat{
			mu:               sync.Mutex{},
			timezone:         timezone,
			ChatStatSnapshot: snapshot}
		return stat, nil
	})
}

func (q *Queries) ChatStatAt(chatId, unixTime int64) *ChatStat {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	timezone := q.GetChatCfgByIdOrDefault(chatId).Timezone
	stat, _ := q.chatStatAtWithTimezone(ctx, chatId, unixTime, timezone)
	return stat
}

func (q *Queries) ChatStatNow(chatId int64) (stat *ChatStat) {
	return q.ChatStatAt(chatId, time.Now().Unix())
}

// ChatStatOfDay returns the stat of the day which contains the unixTime in the chat's timezone.
func (q *Queries) ChatStatOfDay(ctx context.Context, chatId, unixTime int64) (ChatStatSnapshot, int64, error) {
	cfg, err := q.GetChatCfgById(ctx, chatId)
	if err != nil {
		return ChatStatSnapshot{}, 0, err
	}
	daily, err := q.chatStatAtWithTimezone(ctx, chatId, unixTime, cfg.Timezone)
	if err != nil {
		return ChatStatSnapshot{}, 0, err
	}
	return daily.Snapshot(), cfg.Timezone, nil
}
