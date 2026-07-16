package q

import (
	"context"
	"database/sql"
	"testing"

	aischema "main/sql"

	"github.com/PaulSonOfLars/gotgbot/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestUserDimensionCreatesAndRefreshesNames(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	for _, schema := range aischema.Canonical() {
		_, err = database.Exec(schema)
		require.NoError(t, err)
	}
	queries, err := PrepareWithLogger(context.Background(), database, nil)
	require.NoError(t, err)

	tgUser := &gotgbot.User{Id: 42, FirstName: "Alice", LastName: "Old", Username: "alice"}
	user, err := queries.GetOrCreateUserByTg(context.Background(), tgUser)
	require.NoError(t, err)
	require.Equal(t, "Alice Old", user.Name())

	tgUser.FirstName = "Alicia"
	tgUser.LastName = ""
	tgUser.Username = "new_name"
	require.NoError(t, user.TryUpdate(queries, tgUser))
	stored, err := queries.getUserById(context.Background(), tgUser.Id)
	require.NoError(t, err)
	require.Equal(t, "Alicia", stored.FirstName)
	require.False(t, stored.LastName.Valid)
	require.Equal(t, "new_name", stored.Username.String)
}
