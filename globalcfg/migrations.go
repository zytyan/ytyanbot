package g

import (
	"context"
	"database/sql"
	"main/internal/mainmigrations"
)

func runDatabaseMigrations(database *sql.DB) error {
	return mainmigrations.ApplyRuntime(context.Background(), database)
}
