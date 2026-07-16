package dbschema

import (
	"context"
	"database/sql"
	"fmt"
	"main/globalcfg/migrationdefs"
	aischema "main/sql"
)

// Initialize creates the complete canonical schema in an empty database and
// records every migration represented by that schema. It intentionally fails
// on a non-empty database; upgrades belong to the migration tools.
func Initialize(ctx context.Context, database *sql.DB) (err error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var count int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("database is not empty: found %d schema objects", count)
	}
	for _, schema := range aischema.Canonical() {
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return err
		}
	}
	for _, definition := range migrationdefs.All {
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, unixepoch())`, definition.Version, definition.Name,
			migrationdefs.Checksum(definition.Source)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func Validate(ctx context.Context, database *sql.DB) error {
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check: %s", integrity)
	}
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check returned at least one row")
	}
	legacy := []string{
		"gemini_sessions", "gemini_contents", "gemini_system_prompt", "gemini_memories",
		"gemini_messages", "gemini_session_migrations", "ai_chat_models", "ai_session_meta",
		"ai_message_meta", "chat_attr", "chat_topics", "saved_msgs", "raw_update", "edit_history",
		"meili_wal",
	}
	for _, name := range legacy {
		var exists bool
		if err = database.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("retired table remains: %s", name)
		}
	}
	retiredColumns := map[string][]string{
		"chat_cfg":        {"web_id", "auto_ocr", "save_messages"},
		"users":           {"id", "profile_update_at", "profile_photo", "timezone"},
		"chat_stat_daily": {"user_msg_stat", "msg_count_by_time", "msg_id_at_time_start"},
	}
	for table, columns := range retiredColumns {
		for _, column := range columns {
			var exists bool
			if err = database.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM pragma_table_info(?) WHERE name=?)`, table, column).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("retired column remains: %s.%s", table, column)
			}
		}
	}
	return nil
}
