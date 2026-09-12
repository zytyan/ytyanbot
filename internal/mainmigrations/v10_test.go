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

func TestV10CreatesAIMediaGroupRetentionTablesOnline(t *testing.T) {
	database, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "v9.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE schema_migrations(
version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:9] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	require.NoError(t, Apply(context.Background(), database, All(), false))

	_, err = database.Exec(`
INSERT INTO ai_media_groups VALUES(-100, 'album', 10, 11, 20, 1);
INSERT INTO ai_media_group_photos(chat_id,media_group_id,msg_id,sent_at,user_id,username,telegram_file_id)
VALUES(-100,'album',42,10,7,'Alice','file-id');`)
	require.NoError(t, err)
	_, err = database.Exec(`DELETE FROM ai_media_groups WHERE chat_id=-100 AND media_group_id='album'`)
	require.NoError(t, err)
	var count int
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM ai_media_group_photos`).Scan(&count))
	require.Zero(t, count)

	for _, index := range []string{"idx_ai_media_groups_expires_at", "idx_ai_media_group_photos_group"} {
		var exists bool
		require.NoError(t, database.QueryRow(`SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='index' AND name=?)`, index).Scan(&exists))
		require.True(t, exists)
	}
}
