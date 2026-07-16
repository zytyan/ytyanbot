package q

import (
	"context"
	"database/sql"
	"testing"

	aischema "main/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestChatStatSaveAndLoadNormalizedRows(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	for _, schema := range aischema.Canonical() {
		_, err = database.Exec(schema)
		require.NoError(t, err)
	}
	queries := New(database)
	daily, err := queries.createChatStatDaily(context.Background(), -100, 20000)
	require.NoError(t, err)
	stat := &ChatStat{
		timezone: 3600,
		ChatStatSnapshot: ChatStatSnapshot{
			ChatStatDaily: daily,
			UserMsgStat:   make(UserMsgStatMap),
		},
	}
	stat.IncMessage(42, 12, 100, 777)
	stat.IncMessage(42, 8, 200, 778)
	stat.IncPhotoCount()
	require.NoError(t, stat.Save(context.Background(), queries))

	loaded, err := queries.getOrCreateChatStat(context.Background(), -100, 20000)
	require.NoError(t, err)
	require.Equal(t, int64(2), loaded.MessageCount)
	require.Equal(t, int64(1), loaded.PhotoCount)
	require.Equal(t, &UserMsgStat{MsgCount: 2, MsgLen: 20}, loaded.UserMsgStat[42])
	require.Equal(t, int64(2), loaded.MsgCountByTime[6])
	require.Equal(t, int64(777), loaded.MsgIDAtTimeStart[6])
	var bucketCount int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM chat_stat_bucket_daily WHERE chat_id=-100 AND stat_date=20000`).Scan(&bucketCount))
	require.Equal(t, int64(len(loaded.MsgCountByTime)), bucketCount)

	_, err = database.Exec(`DELETE FROM chat_stat_daily WHERE chat_id=-100 AND stat_date=20000`)
	require.NoError(t, err)
	var childCount int64
	require.NoError(t, database.QueryRow(`SELECT
  (SELECT COUNT(*) FROM chat_stat_user_daily) + (SELECT COUNT(*) FROM chat_stat_bucket_daily)`).Scan(&childCount))
	require.Zero(t, childCount)
}
