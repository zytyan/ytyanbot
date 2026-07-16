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

const legacyPictureSchema = `
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE saved_pics(
  file_uid TEXT NOT NULL, file_id TEXT NOT NULL, bot_rate INTEGER NOT NULL, rand_key INTEGER NOT NULL,
  user_rate INTEGER NOT NULL GENERATED ALWAYS AS (
    CASE WHEN rate_user_count>0 THEN CAST(ROUND(user_rating_sum*1.0/rate_user_count) AS INTEGER) ELSE bot_rate END) STORED,
  user_rating_sum INTEGER NOT NULL DEFAULT 0, rate_user_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(file_uid), UNIQUE(user_rate,rand_key), UNIQUE(rand_key)) WITHOUT ROWID, STRICT;
CREATE TABLE saved_pics_rating(
  file_uid TEXT NOT NULL, user_id INTEGER NOT NULL, rating INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(file_uid,user_id), FOREIGN KEY(file_uid) REFERENCES saved_pics(file_uid)) WITHOUT ROWID, STRICT;
CREATE TABLE pic_rate_counter(rate INTEGER NOT NULL, count INTEGER NOT NULL, PRIMARY KEY(rate)) WITHOUT ROWID, STRICT;`

func openV8PictureDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v8.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(legacyPictureSchema)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:8] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	return database
}

func TestV9ConstrainsRatingsAndPreservesValidData(t *testing.T) {
	database := openV8PictureDB(t)
	_, err := database.Exec(`
INSERT INTO saved_pics(file_uid,file_id,bot_rate,rand_key,user_rating_sum,rate_user_count)
VALUES('pic','telegram-file',3,10,6,1);
INSERT INTO saved_pics_rating VALUES('pic',42,6);
INSERT INTO pic_rate_counter VALUES(6,1);`)
	require.NoError(t, err)
	require.ErrorContains(t, ApplyRuntime(context.Background(), database), "requires the offline migration tool")
	require.NoError(t, ApplyOffline(context.Background(), database))

	var botRate, userRate, sum, count int64
	var fileID string
	require.NoError(t, database.QueryRow(`SELECT file_id,bot_rate,user_rate,user_rating_sum,rate_user_count
FROM saved_pics WHERE file_uid='pic'`).Scan(&fileID, &botRate, &userRate, &sum, &count))
	require.Equal(t, "telegram-file", fileID)
	require.Equal(t, []int64{3, 6, 6, 1}, []int64{botRate, userRate, sum, count})
	var rating, counter int64
	require.NoError(t, database.QueryRow(`SELECT rating FROM saved_pics_rating WHERE file_uid='pic' AND user_id=42`).Scan(&rating))
	require.Equal(t, int64(6), rating)
	require.NoError(t, database.QueryRow(`SELECT count FROM pic_rate_counter WHERE rate=6`).Scan(&counter))
	require.Equal(t, int64(1), counter)

	_, err = database.Exec(`INSERT INTO saved_pics(file_uid,file_id,bot_rate,rand_key) VALUES('bad','bad',8,11)`)
	require.ErrorContains(t, err, "CHECK constraint failed")
	_, err = database.Exec(`INSERT INTO saved_pics_rating VALUES('pic',99,8)`)
	require.ErrorContains(t, err, "CHECK constraint failed")
	_, err = database.Exec(`DELETE FROM saved_pics WHERE file_uid='pic'`)
	require.NoError(t, err)
	var ratings int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM saved_pics_rating`).Scan(&ratings))
	require.Zero(t, ratings)
}

func TestV9ReportsInvalidRatingsWithoutChangingData(t *testing.T) {
	database := openV8PictureDB(t)
	_, err := database.Exec(`
INSERT INTO saved_pics(file_uid,file_id,bot_rate,rand_key) VALUES('bad','file',9,20);
INSERT INTO saved_pics_rating VALUES('bad',42,8);`)
	require.NoError(t, err)
	err = ApplyOffline(context.Background(), database)
	require.ErrorContains(t, err, "invalid bot_rate rows=1 values=[9]")
	require.ErrorContains(t, err, "invalid rating rows=1 values=[8]")
	var migrations, pictures, ratings int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=9`).Scan(&migrations))
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM saved_pics`).Scan(&pictures))
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM saved_pics_rating`).Scan(&ratings))
	require.Zero(t, migrations)
	require.Equal(t, int64(1), pictures)
	require.Equal(t, int64(1), ratings)
}
