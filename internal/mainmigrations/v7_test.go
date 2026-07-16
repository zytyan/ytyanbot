package mainmigrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"main/globalcfg/migrationdefs"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestV7AddsYTDLFileLookupIndexOnline(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v6.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE yt_dl_results(
  url TEXT NOT NULL, audio_only INTEGER NOT NULL, resolution INTEGER NOT NULL,
  file_id TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL,
  uploader TEXT NOT NULL, upload_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(url,audio_only,resolution)) WITHOUT ROWID;`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:6] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	require.NoError(t, Apply(context.Background(), database, All()[:7], false))

	rows, err := database.Query(`EXPLAIN QUERY PLAN UPDATE yt_dl_results SET upload_count=upload_count+1 WHERE file_id=?`, "file")
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan.WriteString(detail)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan.String(), "idx_yt_dl_results_file_id")
}
