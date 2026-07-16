package q

import (
	"context"
	"database/sql"
	"testing"

	aischema "main/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestYTDLCacheUpsertRefreshesMetadata(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	for _, schema := range aischema.Canonical() {
		_, err = database.Exec(schema)
		require.NoError(t, err)
	}
	queries := New(database)
	base := UpdateYtDlpCacheParams{
		Url: "https://example.test/video", AudioOnly: false, Resolution: 1080,
		FileID: "old-file", Title: "old title", Description: "old desc", Uploader: "old uploader",
	}
	require.NoError(t, queries.UpdateYtDlpCache(context.Background(), base))
	base.FileID = "new-file"
	base.Title = "new title"
	base.Description = "new desc"
	base.Uploader = "new uploader"
	require.NoError(t, queries.UpdateYtDlpCache(context.Background(), base))
	stored, err := queries.GetYtDlpDbCache(context.Background(), base.Url, base.AudioOnly, base.Resolution)
	require.NoError(t, err)
	require.Equal(t, "new-file", stored.FileID)
	require.Equal(t, "new title", stored.Title)
	require.Equal(t, "new desc", stored.Description)
	require.Equal(t, "new uploader", stored.Uploader)
	require.Equal(t, int64(1), stored.UploadCount)
}
