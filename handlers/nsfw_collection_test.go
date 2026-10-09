package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"main/globalcfg/q"
	"main/helpers/cloudflare"
	aischema "main/sql"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestCollectModeratedPictures(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for _, schema := range aischema.Canonical() {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	queries := q.New(db)
	ctx := context.Background()
	for _, anime := range []bool{false, true} {
		for _, severity := range []int{0, 2, 4, 6} {
			name := fmt.Sprintf("anime=%t-severity=%d", anime, severity)
			t.Run(name, func(t *testing.T) {
				photo := &gotgbot.PhotoSize{FileUniqueId: name, FileId: "file-" + name}
				pic, collected, err := collectModeratedPic(ctx, queries, photo, &cloudflare.Result{Severity: severity, AnimeIllustration: anime})
				require.NoError(t, err)
				require.Equal(t, anime || severity >= 2, collected)
				stored, err := queries.GetNsfwPicByFileUid(ctx, name)
				if collected {
					require.NoError(t, err)
					require.Equal(t, pic, stored)
					require.Equal(t, int64(severity), stored.BotRate)
				} else {
					require.ErrorIs(t, err, sql.ErrNoRows)
				}
			})
		}
	}

	// A newly safe anime picture is collectible, and rescoring retains user votes.
	photo := &gotgbot.PhotoSize{FileUniqueId: "rescore", FileId: "old-file"}
	_, collected, err := collectModeratedPic(ctx, queries, photo, &cloudflare.Result{Severity: 4, AnimeIllustration: true})
	require.NoError(t, err)
	require.True(t, collected)
	_, _, err = queries.RatePic(ctx, "rescore", 42, 6)
	require.NoError(t, err)
	photo.FileId = "new-file"
	pic, collected, err := collectModeratedPic(ctx, queries, photo, &cloudflare.Result{Severity: 0, AnimeIllustration: true})
	require.NoError(t, err)
	require.True(t, collected)
	require.Equal(t, int64(0), pic.BotRate)
	require.Equal(t, int64(6), pic.UserRate)
	require.Equal(t, int64(1), pic.RateUserCount)
	require.Equal(t, "new-file", pic.FileID)

	// Existing membership remains visible even if a new classification isn't collectible.
	pic, collected, err = collectModeratedPic(ctx, queries, photo, &cloudflare.Result{Severity: 0})
	require.NoError(t, err)
	require.True(t, collected)
	require.Equal(t, int64(6), pic.UserRate)
}

func TestCollectModeratedPicDatabaseError(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	for _, anime := range []bool{true, false} {
		_, collected, err := collectModeratedPic(context.Background(), q.New(db), &gotgbot.PhotoSize{FileUniqueId: "picture"}, &cloudflare.Result{Severity: 0, AnimeIllustration: anime})
		require.Error(t, err)
		require.False(t, collected)
	}
}
