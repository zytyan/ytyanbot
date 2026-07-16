package q

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	aischema "main/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestBiliInlineRetention(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	for _, schema := range aischema.Canonical() {
		_, err = database.Exec(schema)
		require.NoError(t, err)
	}
	queries := New(database)
	uid, err := queries.CreateBiliInlineData(context.Background(), UnixTime{Time: time.Unix(1_000, 0)})
	require.NoError(t, err)
	require.NoError(t, queries.UpdateBiliInlineMsgId(context.Background(), "BV1", -100, 42, uid))
	row, err := queries.GetBiliInlineData(context.Background(), uid, UnixTime{Time: time.Unix(1_000, 0)})
	require.NoError(t, err)
	require.Equal(t, "BV1", row.Text)
	_, err = queries.GetBiliInlineData(context.Background(), uid, UnixTime{Time: time.Unix(1_001, 0)})
	require.True(t, errors.Is(err, sql.ErrNoRows))

	deleted, err := queries.DeleteExpiredBiliInlineData(context.Background(), UnixTime{Time: time.Unix(1_001, 0)})
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	_, err = queries.GetBiliInlineData(context.Background(), uid, UnixTime{Time: time.Unix(0, 0)})
	require.True(t, errors.Is(err, sql.ErrNoRows))
}
