package legacyarchive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestRunArchivesAllLegacyArtifacts(t *testing.T) {
	dir := t.TempDir()
	messagePath := filepath.Join(dir, "messages-source.db")
	messageDB, err := sql.Open("sqlite3", messagePath)
	require.NoError(t, err)
	_, err = messageDB.Exec(`
CREATE TABLE saved_msgs(chat_id INTEGER, message_id INTEGER);
CREATE TABLE raw_update(id INTEGER);
CREATE TABLE edit_history(chat_id INTEGER);
INSERT INTO saved_msgs VALUES (1, 2);`)
	require.NoError(t, err)
	require.NoError(t, messageDB.Close())

	walPath := filepath.Join(dir, "wal-source.db")
	walDB, err := sql.Open("sqlite3", walPath)
	require.NoError(t, err)
	_, err = walDB.Exec(`CREATE TABLE meili_wal(id INTEGER); INSERT INTO meili_wal VALUES (1), (2);`)
	require.NoError(t, err)
	require.NoError(t, walDB.Close())
	dumpPath := filepath.Join(dir, "messages.dump")
	require.NoError(t, os.WriteFile(dumpPath, []byte("meili dump"), 0600))

	output := filepath.Join(dir, "archive")
	manifest, err := Run(context.Background(), Config{
		MessageDB: messagePath, WALDB: walPath, MeiliDump: dumpPath, OutputDir: output,
	})
	require.NoError(t, err)
	require.Len(t, manifest.Artifacts, 3)
	require.Equal(t, int64(1), manifest.Artifacts[0].TableCounts["saved_msgs"])
	require.Equal(t, int64(2), manifest.Artifacts[1].TableCounts["meili_wal"])
	require.FileExists(t, filepath.Join(output, "manifest.json"))
}

func TestRunRecordsUnavailableMeiliDump(t *testing.T) {
	dir := t.TempDir()
	messagePath := filepath.Join(dir, "messages.db")
	messageDB, err := sql.Open("sqlite3", messagePath)
	require.NoError(t, err)
	_, err = messageDB.Exec(`CREATE TABLE saved_msgs(id INTEGER); CREATE TABLE raw_update(id INTEGER); CREATE TABLE edit_history(id INTEGER);`)
	require.NoError(t, err)
	require.NoError(t, messageDB.Close())
	walPath := filepath.Join(dir, "wal.db")
	walDB, err := sql.Open("sqlite3", walPath)
	require.NoError(t, err)
	_, err = walDB.Exec(`CREATE TABLE meili_wal(id INTEGER);`)
	require.NoError(t, err)
	require.NoError(t, walDB.Close())
	manifest, err := Run(context.Background(), Config{
		MessageDB: messagePath, WALDB: walPath, OutputDir: filepath.Join(dir, "archive"),
		MeiliDumpUnavailableReason: "service and data directory do not exist",
	})
	require.NoError(t, err)
	require.Equal(t, "unavailable", manifest.Artifacts[2].Status)
	require.Equal(t, "service and data directory do not exist", manifest.Artifacts[2].UnavailableReason)
}
