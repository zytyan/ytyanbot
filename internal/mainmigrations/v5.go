package mainmigrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
)

const statBucketCount = 24 * 6

type legacyUserStat struct {
	MsgCount int64
	MsgLen   int64
}

type legacyDailyStat struct {
	chatID        int64
	statDate      int64
	users         map[int64]*legacyUserStat
	messageCounts [statBucketCount]int64
	firstMsgIDs   [statBucketCount]int64
}

func decodeLegacyGob(data []byte, target any) error {
	if len(data) == 0 {
		return nil
	}
	return gob.NewDecoder(bytes.NewReader(data)).Decode(target)
}

func loadLegacyDailyStats(ctx context.Context, tx *sql.Tx) ([]legacyDailyStat, error) {
	rows, err := tx.QueryContext(ctx, `SELECT chat_id, stat_date, user_msg_stat,
msg_count_by_time, msg_id_at_time_start FROM chat_stat_daily ORDER BY chat_id, stat_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []legacyDailyStat
	for rows.Next() {
		var stat legacyDailyStat
		var users, counts, firstIDs []byte
		if err = rows.Scan(&stat.chatID, &stat.statDate, &users, &counts, &firstIDs); err != nil {
			return nil, err
		}
		stat.users = make(map[int64]*legacyUserStat)
		if err = decodeLegacyGob(users, &stat.users); err != nil {
			return nil, fmt.Errorf("decode user stats for chat %d day %d: %w", stat.chatID, stat.statDate, err)
		}
		if err = decodeLegacyGob(counts, &stat.messageCounts); err != nil {
			return nil, fmt.Errorf("decode message buckets for chat %d day %d: %w", stat.chatID, stat.statDate, err)
		}
		if err = decodeLegacyGob(firstIDs, &stat.firstMsgIDs); err != nil {
			return nil, fmt.Errorf("decode first-message buckets for chat %d day %d: %w", stat.chatID, stat.statDate, err)
		}
		for userID, user := range stat.users {
			if user == nil {
				return nil, fmt.Errorf("nil user stat for chat %d day %d user %d", stat.chatID, stat.statDate, userID)
			}
			if user.MsgCount < 0 || user.MsgLen < 0 {
				return nil, fmt.Errorf("negative user stat for chat %d day %d user %d", stat.chatID, stat.statDate, userID)
			}
		}
		for bucket, count := range stat.messageCounts {
			if count < 0 {
				return nil, fmt.Errorf("negative bucket %d for chat %d day %d", bucket, stat.chatID, stat.statDate)
			}
		}
		result = append(result, stat)
	}
	return result, rows.Err()
}

func migrateNormalizeChatStats(ctx context.Context, tx *sql.Tx) error {
	legacy, err := loadLegacyDailyStats(ctx, tx)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
CREATE TABLE chat_stat_daily_v5
(
    chat_id INTEGER NOT NULL,
    stat_date INTEGER NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0,
    photo_count INTEGER NOT NULL DEFAULT 0,
    video_count INTEGER NOT NULL DEFAULT 0,
    sticker_count INTEGER NOT NULL DEFAULT 0,
    forward_count INTEGER NOT NULL DEFAULT 0,
    mars_count INTEGER NOT NULL DEFAULT 0,
    max_mars_count INTEGER NOT NULL DEFAULT 0,
    racy_count INTEGER NOT NULL DEFAULT 0,
    adult_count INTEGER NOT NULL DEFAULT 0,
    download_video_count INTEGER NOT NULL DEFAULT 0,
    download_audio_count INTEGER NOT NULL DEFAULT 0,
    dio_add_user_count INTEGER NOT NULL DEFAULT 0,
    dio_ban_user_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, stat_date)
) WITHOUT ROWID;

INSERT INTO chat_stat_daily_v5
SELECT chat_id, stat_date, message_count, photo_count, video_count, sticker_count,
       forward_count, mars_count, max_mars_count, racy_count, adult_count,
       download_video_count, download_audio_count, dio_add_user_count, dio_ban_user_count
FROM chat_stat_daily;
DROP TABLE chat_stat_daily;
ALTER TABLE chat_stat_daily_v5 RENAME TO chat_stat_daily;

CREATE TABLE chat_stat_user_daily
(
    chat_id INTEGER NOT NULL,
    stat_date INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    message_count INTEGER NOT NULL CHECK (message_count >= 0),
    message_length INTEGER NOT NULL CHECK (message_length >= 0),
    PRIMARY KEY (chat_id, stat_date, user_id),
    FOREIGN KEY (chat_id, stat_date) REFERENCES chat_stat_daily(chat_id, stat_date) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;

CREATE TABLE chat_stat_bucket_daily
(
    chat_id INTEGER NOT NULL,
    stat_date INTEGER NOT NULL,
    bucket INTEGER NOT NULL CHECK (bucket BETWEEN 0 AND 143),
    message_count INTEGER NOT NULL CHECK (message_count >= 0),
    first_msg_id INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, stat_date, bucket),
    FOREIGN KEY (chat_id, stat_date) REFERENCES chat_stat_daily(chat_id, stat_date) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;
`); err != nil {
		return err
	}
	userInsert, err := tx.PrepareContext(ctx, `INSERT INTO chat_stat_user_daily
(chat_id,stat_date,user_id,message_count,message_length) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer userInsert.Close()
	bucketInsert, err := tx.PrepareContext(ctx, `INSERT INTO chat_stat_bucket_daily
(chat_id,stat_date,bucket,message_count,first_msg_id) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer bucketInsert.Close()
	for _, stat := range legacy {
		for userID, user := range stat.users {
			if _, err = userInsert.ExecContext(ctx, stat.chatID, stat.statDate, userID, user.MsgCount, user.MsgLen); err != nil {
				return err
			}
		}
		for bucket := range statBucketCount {
			if _, err = bucketInsert.ExecContext(ctx, stat.chatID, stat.statDate, bucket,
				stat.messageCounts[bucket], stat.firstMsgIDs[bucket]); err != nil {
				return err
			}
		}
	}
	return validateNormalizedChatStats(ctx, tx, legacy)
}

func validateNormalizedChatStats(ctx context.Context, tx *sql.Tx, legacy []legacyDailyStat) error {
	for _, stat := range legacy {
		users, err := tx.QueryContext(ctx, `SELECT user_id,message_count,message_length
FROM chat_stat_user_daily WHERE chat_id=? AND stat_date=? ORDER BY user_id`, stat.chatID, stat.statDate)
		if err != nil {
			return err
		}
		seenUsers := 0
		for users.Next() {
			var userID, count, length int64
			if err = users.Scan(&userID, &count, &length); err != nil {
				_ = users.Close()
				return err
			}
			expected, ok := stat.users[userID]
			if !ok || expected.MsgCount != count || expected.MsgLen != length {
				_ = users.Close()
				return fmt.Errorf("user stat mismatch for chat %d day %d user %d", stat.chatID, stat.statDate, userID)
			}
			seenUsers++
		}
		if err = users.Close(); err != nil {
			return err
		}
		if seenUsers != len(stat.users) {
			return fmt.Errorf("user stat count mismatch for chat %d day %d", stat.chatID, stat.statDate)
		}
		buckets, err := tx.QueryContext(ctx, `SELECT bucket,message_count,first_msg_id
FROM chat_stat_bucket_daily WHERE chat_id=? AND stat_date=? ORDER BY bucket`, stat.chatID, stat.statDate)
		if err != nil {
			return err
		}
		seenBuckets := 0
		for buckets.Next() {
			var bucket int
			var count, firstID int64
			if err = buckets.Scan(&bucket, &count, &firstID); err != nil {
				_ = buckets.Close()
				return err
			}
			if bucket != seenBuckets || count != stat.messageCounts[bucket] || firstID != stat.firstMsgIDs[bucket] {
				_ = buckets.Close()
				return fmt.Errorf("bucket mismatch for chat %d day %d bucket %d", stat.chatID, stat.statDate, bucket)
			}
			seenBuckets++
		}
		if err = buckets.Close(); err != nil {
			return err
		}
		if seenBuckets != statBucketCount {
			return fmt.Errorf("bucket count mismatch for chat %d day %d: %d", stat.chatID, stat.statDate, seenBuckets)
		}
	}
	return nil
}
