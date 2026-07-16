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

func TestV8BackfillsBiliInlineCreationTimeOnline(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v7.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE bili_inline_results(
  uid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, text TEXT NOT NULL DEFAULT '',
  chat_id INTEGER NOT NULL DEFAULT 0, msg_id INTEGER NOT NULL DEFAULT 0);
INSERT INTO bili_inline_results(text,chat_id,msg_id) VALUES('legacy',-100,42);`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:7] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	before := timeNowUnix(t, database)
	require.NoError(t, Apply(context.Background(), database, All()[:8], false))
	after := timeNowUnix(t, database)

	var createdAt int64
	require.NoError(t, database.QueryRow(`SELECT created_at FROM bili_inline_results WHERE uid=1`).Scan(&createdAt))
	require.GreaterOrEqual(t, createdAt, before)
	require.LessOrEqual(t, createdAt, after)
	var exists bool
	require.NoError(t, database.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master
WHERE type='index' AND name='idx_bili_inline_results_created_at')`).Scan(&exists))
	require.True(t, exists)
}

func timeNowUnix(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var now int64
	require.NoError(t, database.QueryRow(`SELECT unixepoch()`).Scan(&now))
	return now
}
