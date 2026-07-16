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

func TestV6AddsAIMessageSessionLookupIndexOnline(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v5.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT;
CREATE TABLE ai_session_messages(
  session_id INTEGER NOT NULL, position INTEGER NOT NULL, chat_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL, role TEXT NOT NULL, quote_part TEXT, context_only INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(session_id, position), UNIQUE(session_id, chat_id, msg_id)) WITHOUT ROWID, STRICT;`)
	require.NoError(t, err)
	for _, definition := range migrationdefs.All[:5] {
		_, err = database.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,unixepoch())`,
			definition.Version, definition.Name, migrationdefs.Checksum(definition.Source))
		require.NoError(t, err)
	}
	require.NoError(t, ApplyRuntime(context.Background(), database))

	var exists bool
	require.NoError(t, database.QueryRow(`SELECT EXISTS(
  SELECT 1 FROM sqlite_master WHERE type='index' AND name='idx_ai_session_messages_chat_msg_context')`).Scan(&exists))
	require.True(t, exists)
	rows, err := database.Query(`EXPLAIN QUERY PLAN SELECT session_id FROM ai_session_messages
WHERE chat_id=? AND msg_id=? AND context_only=0`, -100, 42)
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
	require.Contains(t, plan.String(), "idx_ai_session_messages_chat_msg_context")
}
