package dbschema

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestInitializeCreatesOnlyCanonicalTables(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "main.db")+"?_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, Initialize(context.Background(), database))
	require.NoError(t, Validate(context.Background(), database))

	var migrations int64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations))
	require.Equal(t, int64(3), migrations)
	require.ErrorContains(t, Initialize(context.Background(), database), "not empty")
}
