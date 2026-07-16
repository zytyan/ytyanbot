package mainmigrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"main/globalcfg/migrationdefs"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestV4CleanupPreservesActiveUserAndChatData(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-v3.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, updated_at INT_UNIX_SEC NOT NULL,
  user_id INTEGER NOT NULL UNIQUE, first_name TEXT NOT NULL, last_name TEXT, username TEXT,
  profile_update_at INT_UNIX_SEC NOT NULL, profile_photo TEXT, timezone INTEGER NOT NULL DEFAULT 480);
CREATE TABLE chat_cfg(id INTEGER PRIMARY KEY NOT NULL, web_id INTEGER,
  auto_cvt_bili INT_BOOL NOT NULL, auto_ocr INT_BOOL NOT NULL, auto_calculate INT_BOOL NOT NULL,
  auto_exchange INT_BOOL NOT NULL, auto_check_adult INT_BOOL NOT NULL, save_messages INT_BOOL NOT NULL,
  enable_coc INT_BOOL NOT NULL, resp_nsfw_msg INT_BOOL NOT NULL, timezone INTEGER NOT NULL);
CREATE INDEX idx_chat_cfg ON chat_cfg(web_id);
CREATE TABLE chat_attr(id INTEGER PRIMARY KEY, type TEXT NOT NULL);
CREATE TABLE chat_topics(chat_id INTEGER, thread_id INTEGER, name TEXT, PRIMARY KEY(chat_id, thread_id));
INSERT INTO users(updated_at,user_id,first_name,last_name,username,profile_update_at,profile_photo,timezone)
VALUES(10,42,'Alice','Example','alice',11,'photo',28800);
INSERT INTO chat_cfg VALUES(-100,7,1,1,1,0,1,1,1,0,28800);
INSERT INTO chat_attr VALUES(-100,'supergroup');
INSERT INTO chat_topics VALUES(-100,1,'topic');`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:int(migrationdefs.AIV2Version)] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	require.ErrorContains(t, ApplyRuntime(context.Background(), database), "requires the offline migration tool")
	require.NoError(t, Apply(context.Background(), database, All()[:4], true))

	var userID, updatedAt int64
	var firstName, lastName, username string
	require.NoError(t, database.QueryRow(`SELECT user_id,updated_at,first_name,last_name,username FROM users`).
		Scan(&userID, &updatedAt, &firstName, &lastName, &username))
	require.Equal(t, int64(42), userID)
	require.Equal(t, "Alice", firstName)

	var autoCvt, autoCalc, autoAdult, enableCOC bool
	var timezone int64
	require.NoError(t, database.QueryRow(`SELECT auto_cvt_bili,auto_calculate,auto_check_adult,enable_coc,timezone FROM chat_cfg`).
		Scan(&autoCvt, &autoCalc, &autoAdult, &enableCOC, &timezone))
	require.True(t, autoCvt)
	require.True(t, autoCalc)
	require.True(t, autoAdult)
	require.True(t, enableCOC)
	require.Equal(t, int64(28800), timezone)
	for _, table := range []string{"chat_attr", "chat_topics"} {
		var exists bool
		require.NoError(t, database.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists))
		require.False(t, exists)
	}
}
