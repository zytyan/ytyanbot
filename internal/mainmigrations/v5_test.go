package mainmigrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"path/filepath"
	"testing"

	"main/globalcfg/migrationdefs"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

type fixtureUserStat struct {
	MsgCount int64
	MsgLen   int64
}

func encodeGob(t *testing.T, value any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buffer).Encode(value))
	return buffer.Bytes()
}

func TestV5NormalizesEveryLegacyChatStatistic(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-v4.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE chat_stat_daily(
  chat_id INTEGER NOT NULL, stat_date INTEGER NOT NULL,
  message_count INTEGER NOT NULL DEFAULT 0, photo_count INTEGER NOT NULL DEFAULT 0,
  video_count INTEGER NOT NULL DEFAULT 0, sticker_count INTEGER NOT NULL DEFAULT 0,
  forward_count INTEGER NOT NULL DEFAULT 0, mars_count INTEGER NOT NULL DEFAULT 0,
  max_mars_count INTEGER NOT NULL DEFAULT 0, racy_count INTEGER NOT NULL DEFAULT 0,
  adult_count INTEGER NOT NULL DEFAULT 0, download_video_count INTEGER NOT NULL DEFAULT 0,
  download_audio_count INTEGER NOT NULL DEFAULT 0, dio_add_user_count INTEGER NOT NULL DEFAULT 0,
  dio_ban_user_count INTEGER NOT NULL DEFAULT 0, user_msg_stat BLOB NOT NULL,
  msg_count_by_time BLOB NOT NULL, msg_id_at_time_start BLOB NOT NULL,
  PRIMARY KEY(chat_id, stat_date)) WITHOUT ROWID;`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:4] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	users := map[int64]*fixtureUserStat{
		42: {MsgCount: 7, MsgLen: 321},
		99: {MsgCount: 2, MsgLen: 18},
	}
	var counts, firstIDs [statBucketCount]int64
	counts[0], counts[17], counts[143] = 2, 5, 2
	firstIDs[0], firstIDs[17], firstIDs[143] = 1001, 1010, 1099
	_, err = database.Exec(`INSERT INTO chat_stat_daily VALUES(
    -100,20000,9,3,1,2,1,4,2,1,1,5,6,7,8,?,?,?)`,
		encodeGob(t, users), encodeGob(t, counts), encodeGob(t, firstIDs))
	require.NoError(t, err)

	require.ErrorContains(t, ApplyRuntime(context.Background(), database), "requires the offline migration tool")
	require.NoError(t, Apply(context.Background(), database, All()[:5], true))

	var messageCount, photoCount int64
	require.NoError(t, database.QueryRow(`SELECT message_count,photo_count FROM chat_stat_daily WHERE chat_id=-100 AND stat_date=20000`).
		Scan(&messageCount, &photoCount))
	require.Equal(t, int64(9), messageCount)
	require.Equal(t, int64(3), photoCount)
	var userCount, bucketCount int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM chat_stat_user_daily`).Scan(&userCount))
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM chat_stat_bucket_daily`).Scan(&bucketCount))
	require.Equal(t, int64(2), userCount)
	require.Equal(t, int64(statBucketCount), bucketCount)
	var storedCount, storedLength int64
	require.NoError(t, database.QueryRow(`SELECT message_count,message_length FROM chat_stat_user_daily WHERE user_id=42`).
		Scan(&storedCount, &storedLength))
	require.Equal(t, int64(7), storedCount)
	require.Equal(t, int64(321), storedLength)
	var firstID int64
	require.NoError(t, database.QueryRow(`SELECT message_count,first_msg_id FROM chat_stat_bucket_daily WHERE bucket=17`).
		Scan(&storedCount, &firstID))
	require.Equal(t, int64(5), storedCount)
	require.Equal(t, int64(1010), firstID)
	for _, removed := range []string{"user_msg_stat", "msg_count_by_time", "msg_id_at_time_start"} {
		var exists bool
		require.NoError(t, database.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('chat_stat_daily') WHERE name=?)`, removed).Scan(&exists))
		require.False(t, exists)
	}
	var violations int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations))
	require.Zero(t, violations)
}
