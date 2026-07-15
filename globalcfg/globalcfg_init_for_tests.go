package g

import (
	"database/sql"
	"main/globalcfg/migrationdefs"
	"os"
	"path/filepath"
	"strings"
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
	projRoot := mustGetProjectRootDir()
	sqlDir := filepath.Join(projRoot, "sql")
	dir, err := os.ReadDir(sqlDir)
	if err != nil {
		panic(err)
	}
	for _, file := range dir {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if !strings.HasSuffix(name, ".sql") || !strings.HasPrefix(name, "schema_") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sqlDir, name))
		if err != nil {
			panic(err)
		}
		_, err = database.Exec(string(data))
		if err != nil {
			panic(err)
		}
	}
	for _, definition := range migrationdefs.All {
		_, err = database.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, unixepoch())`, definition.Version, definition.Name,
			migrationdefs.Checksum(definition.Source))
		if err != nil {
			panic(err)
		}
	}
}
