package g

import (
	"context"
	"database/sql"
	"main/globalcfg/migrationdefs"
	aischema "main/sql"
	"os"
	"path/filepath"
)

func mustGetProjectRootDir() string {
	current, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		parent := filepath.Dir(current)
		modFile := filepath.Join(parent, "go.mod")
		if stat, err := os.Stat(modFile); err == nil && !stat.IsDir() {
			return parent
		}
		if current == "/" {
			panic(modFile)
		}
		current = parent
	}
}

func initMainDatabaseInMemory(database *sql.DB) {
	for _, schema := range aischema.Canonical() {
		_, err := database.ExecContext(context.Background(), schema)
		if err != nil {
			panic(err)
		}
	}
	for _, definition := range migrationdefs.All {
		_, err := database.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, unixepoch())`, definition.Version, definition.Name,
			migrationdefs.Checksum(definition.Source))
		if err != nil {
			panic(err)
		}
	}
}
