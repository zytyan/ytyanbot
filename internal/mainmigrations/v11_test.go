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

func TestV11CreatesAIUserReactionSettingsOnline(t *testing.T) {
	database, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "v10.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE schema_migrations(
version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:10] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	require.NoError(t, Apply(context.Background(), database, All(), false))

	_, err = database.Exec(`INSERT INTO ai_user_settings(user_id,reactions_enabled,updated_at) VALUES(42,0,100)`)
	require.NoError(t, err)
	var enabled int64
	require.NoError(t, database.QueryRow(`SELECT reactions_enabled FROM ai_user_settings WHERE user_id=42`).Scan(&enabled))
	require.Zero(t, enabled)
	_, err = database.Exec(`INSERT INTO ai_user_settings(user_id,reactions_enabled,updated_at) VALUES(43,2,100)`)
	require.Error(t, err)
}
