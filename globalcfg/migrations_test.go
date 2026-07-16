package g

import (
	"context"
	"database/sql"
	"errors"
	"main/internal/mainmigrations"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "migration.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func TestDatabaseMigrationsAreIdempotentAndChecksummed(t *testing.T) {
	database := openMigrationTestDB(t)
	migrations := []mainmigrations.Migration{{
		Version: 1,
		Name:    "create_example",
		Source:  "CREATE TABLE example(id INTEGER PRIMARY KEY)",
		Run: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE example(id INTEGER PRIMARY KEY)`)
			return err
		},
	}}
	require.NoError(t, mainmigrations.Apply(context.Background(), database, migrations, false))
	require.NoError(t, mainmigrations.Apply(context.Background(), database, migrations, false))

	var count int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, 1, count)

	changed := append([]mainmigrations.Migration(nil), migrations...)
	changed[0].Source += " -- changed"
	err := mainmigrations.Apply(context.Background(), database, changed, false)
	require.ErrorContains(t, err, "checksum mismatch")
}

func TestDatabaseMigrationRollbackAndOfflineGate(t *testing.T) {
	database := openMigrationTestDB(t)
	failing := []mainmigrations.Migration{{
		Version: 1,
		Name:    "rollback_example",
		Source:  "rollback example",
		Run: func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE rolled_back(id INTEGER)`); err != nil {
				return err
			}
			return errors.New("stop")
		},
	}}
	require.ErrorContains(t, mainmigrations.Apply(context.Background(), database, failing, false), "stop")
	var exists bool
	require.NoError(t, database.QueryRow(`SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name='rolled_back')`).Scan(&exists))
	require.False(t, exists)

	offline := []mainmigrations.Migration{{
		Version: 1, Name: "offline", Source: "offline", Offline: true,
		Run: func(context.Context, *sql.Tx) error { return nil },
	}}
	require.ErrorContains(t, mainmigrations.Apply(context.Background(), database, offline, false),
		"requires the offline migration tool")
}

func TestMainDatabaseV3RequiresOfflineMigrationTool(t *testing.T) {
	database := openMigrationTestDB(t)
	err := mainmigrations.Apply(context.Background(), database, mainmigrations.All()[2:], false)
	require.ErrorContains(t, err, "generic_ai_v2")
	require.ErrorContains(t, err, "requires the offline migration tool")
}

func TestSQLitePoolConnectionPragmas(t *testing.T) {
	database := getSqliteConn(filepath.Join(t.TempDir(), "pool.db"))
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	connections := make([]*sql.Conn, 0, 4)
	for range 4 {
		conn, err := database.Conn(context.Background())
		require.NoError(t, err)
		connections = append(connections, conn)
		var foreignKeys, busyTimeout, synchronous int
		var journalMode string
		require.NoError(t, conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys))
		require.NoError(t, conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout))
		require.NoError(t, conn.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode))
		require.NoError(t, conn.QueryRowContext(context.Background(), `PRAGMA synchronous`).Scan(&synchronous))
		require.Equal(t, 1, foreignKeys)
		require.Equal(t, 5000, busyTimeout)
		require.Equal(t, "wal", journalMode)
		require.Equal(t, 1, synchronous)
	}
	for _, conn := range connections {
		require.NoError(t, conn.Close())
	}

	_, err := database.Exec(`CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE child(parent_id INTEGER NOT NULL REFERENCES parent(id));`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO child(parent_id) VALUES (99)`)
	require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
}
