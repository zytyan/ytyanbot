package maindbmigrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"main/internal/dbschema"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestRunCopiesCanonicalDatabaseWithoutTouchingSource(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	database, err := sql.Open("sqlite3", sourcePath+"?_foreign_keys=on")
	require.NoError(t, err)
	require.NoError(t, dbschema.Initialize(context.Background(), database))
	_, err = database.Exec(`INSERT INTO character_attrs(user_id, attr_name, attr_value) VALUES (1, 'str', '50')`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	outputPath := filepath.Join(dir, "output.db")
	manifest, err := Run(context.Background(), Config{Source: sourcePath, Output: outputPath})
	require.NoError(t, err)
	require.True(t, manifest.SourceUnchanged)
	require.Equal(t, int64(1), manifest.SourceCounts["character_attrs"])
	require.Equal(t, manifest.SourceCounts, manifest.TargetCounts)
	require.FileExists(t, outputPath)
	require.FileExists(t, outputPath+".manifest.json")
}
