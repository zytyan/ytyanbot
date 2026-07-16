package maindbmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"main/globalcfg/aiq"
	"main/globalcfg/migrationdefs"
	"main/internal/dbschema"
	"main/internal/mainmigrations"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type Config struct {
	Source       string
	Output       string
	ManifestPath string
}

type Manifest struct {
	StartedAt        time.Time        `json:"started_at"`
	CompletedAt      time.Time        `json:"completed_at"`
	SourcePath       string           `json:"source_path"`
	OutputPath       string           `json:"output_path"`
	SourceSHA256     string           `json:"source_sha256"`
	OutputSHA256     string           `json:"output_sha256"`
	SourceCounts     map[string]int64 `json:"source_counts"`
	TargetCounts     map[string]int64 `json:"target_counts"`
	SourceAI         AIStats          `json:"source_ai"`
	TargetAI         AIStats          `json:"target_ai"`
	IntegrityCheck   string           `json:"integrity_check"`
	ForeignKeyIssues int64            `json:"foreign_key_issues"`
	SourceUnchanged  bool             `json:"source_unchanged"`
}

type AIStats struct {
	Sessions          int64 `json:"sessions"`
	Messages          int64 `json:"messages"`
	SessionMessages   int64 `json:"session_messages"`
	Runs              int64 `json:"runs"`
	Prompts           int64 `json:"prompts"`
	ChatSettings      int64 `json:"chat_settings"`
	MediaObjects      int64 `json:"media_objects"`
	MediaReferences   int64 `json:"media_references"`
	AssistantPayloads int64 `json:"assistant_payloads"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
}

func Run(ctx context.Context, cfg Config) (manifest Manifest, err error) {
	manifest.StartedAt = time.Now().UTC()
	cfg, err = normalize(cfg)
	if err != nil {
		return manifest, err
	}
	manifest.SourcePath, manifest.OutputPath = cfg.Source, cfg.Output
	if err = ensureAbsent(cfg.Output, cfg.ManifestPath); err != nil {
		return manifest, err
	}
	manifest.SourceSHA256, err = fileSHA256(cfg.Source)
	if err != nil {
		return manifest, err
	}
	source, err := sql.Open("sqlite3", readOnlyDSN(cfg.Source))
	if err != nil {
		return manifest, err
	}
	defer source.Close()
	if err = requireAIV2(ctx, source); err != nil {
		return manifest, err
	}
	manifest.SourceCounts, err = tableCounts(ctx, source)
	if err != nil {
		return manifest, err
	}
	manifest.SourceAI, err = readAIStats(ctx, source)
	if err != nil {
		return manifest, err
	}
	stagingPath := cfg.Output + ".staging"
	finalPath := cfg.Output + ".vacuum"
	if err = ensureAbsent(stagingPath, finalPath); err != nil {
		return manifest, err
	}
	defer os.Remove(stagingPath)
	defer os.Remove(finalPath)
	staging, err := sql.Open("sqlite3", writableDSN(stagingPath))
	if err != nil {
		return manifest, err
	}
	if err = backupDatabase(ctx, source, staging); err == nil {
		err = mainmigrations.ApplyOffline(ctx, staging)
	}
	if err == nil {
		err = dbschema.Validate(ctx, staging)
	}
	if err == nil {
		manifest.TargetCounts, err = tableCounts(ctx, staging)
	}
	if err == nil {
		err = validatePreservedCounts(manifest.SourceCounts, manifest.TargetCounts)
	}
	if err == nil {
		manifest.TargetAI, err = readAIStats(ctx, staging)
	}
	if err == nil && manifest.TargetAI != manifest.SourceAI {
		err = fmt.Errorf("AI counts or tokens changed: source=%+v target=%+v", manifest.SourceAI, manifest.TargetAI)
	}
	if err == nil {
		_, err = staging.ExecContext(ctx, `VACUUM INTO ?`, finalPath)
	}
	if closeErr := staging.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return manifest, err
	}
	if err = validateFile(ctx, finalPath, &manifest); err != nil {
		return manifest, err
	}
	if err = os.Rename(finalPath, cfg.Output); err != nil {
		return manifest, err
	}
	manifest.OutputSHA256, err = fileSHA256(cfg.Output)
	if err != nil {
		return manifest, err
	}
	afterSource, err := fileSHA256(cfg.Source)
	if err != nil {
		return manifest, err
	}
	manifest.SourceUnchanged = afterSource == manifest.SourceSHA256
	if !manifest.SourceUnchanged {
		return manifest, errors.New("source database changed during migration")
	}
	manifest.CompletedAt = time.Now().UTC()
	if err = writeManifest(cfg.ManifestPath, manifest); err != nil {
		_ = os.Remove(cfg.Output)
		return manifest, err
	}
	return manifest, nil
}

func normalize(cfg Config) (Config, error) {
	if cfg.Source == "" || cfg.Output == "" {
		return cfg, errors.New("source and output are required")
	}
	var err error
	if cfg.Source, err = filepath.Abs(cfg.Source); err != nil {
		return cfg, err
	}
	if cfg.Output, err = filepath.Abs(cfg.Output); err != nil {
		return cfg, err
	}
	if cfg.Source == cfg.Output {
		return cfg, errors.New("output must differ from source")
	}
	if cfg.ManifestPath == "" {
		cfg.ManifestPath = cfg.Output + ".manifest.json"
	}
	if cfg.ManifestPath, err = filepath.Abs(cfg.ManifestPath); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func ensureAbsent(paths ...string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("target already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readOnlyDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	q.Set("_foreign_keys", "on")
	u.RawQuery = q.Encode()
	return u.String()
}

func writableDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rwc")
	q.Set("_foreign_keys", "on")
	u.RawQuery = q.Encode()
	return u.String()
}

func requireAIV2(ctx context.Context, database *sql.DB) error {
	definition := migrationdefs.All[2]
	var name, checksum string
	if err := database.QueryRowContext(ctx,
		`SELECT name, checksum FROM schema_migrations WHERE version=?`, definition.Version).
		Scan(&name, &checksum); err != nil {
		return fmt.Errorf("source must first pass ai-db-migrate: %w", err)
	}
	if name != definition.Name || checksum != migrationdefs.Checksum(definition.Source) {
		return errors.New("source AI V2 migration marker does not match this build")
	}
	return nil
}

func backupDatabase(ctx context.Context, source, destination *sql.DB) error {
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()
	destinationConn, err := destination.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()
	return destinationConn.Raw(func(destinationDriver any) error {
		destinationSQLite, ok := destinationDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected destination driver %T", destinationDriver)
		}
		return sourceConn.Raw(func(sourceDriver any) error {
			sourceSQLite, ok := sourceDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected source driver %T", sourceDriver)
			}
			backup, err := destinationSQLite.Backup("main", sourceSQLite, "main")
			if err != nil {
				return err
			}
			defer backup.Finish()
			for {
				done, stepErr := backup.Step(256)
				if stepErr != nil {
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				if err = ctx.Err(); err != nil {
					return err
				}
			}
		})
	})
}

func tableCounts(ctx context.Context, database *sql.DB) (map[string]int64, error) {
	rows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_master
WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(names))
	for _, name := range names {
		if strings.ContainsAny(name, `"'`) {
			return nil, fmt.Errorf("unsafe table name %q", name)
		}
		var count int64
		if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+name+`"`).Scan(&count); err != nil {
			return nil, err
		}
		counts[name] = count
	}
	return counts, nil
}

func validateFile(ctx context.Context, path string, manifest *Manifest) error {
	database, err := sql.Open("sqlite3", readOnlyDSN(path))
	if err != nil {
		return err
	}
	defer database.Close()
	if err = database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&manifest.IntegrityCheck); err != nil {
		return err
	}
	if manifest.IntegrityCheck != "ok" {
		return fmt.Errorf("integrity_check: %s", manifest.IntegrityCheck)
	}
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	for rows.Next() {
		manifest.ForeignKeyIssues++
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if manifest.ForeignKeyIssues != 0 {
		return fmt.Errorf("foreign_key_check returned %d rows", manifest.ForeignKeyIssues)
	}
	counts, err := tableCounts(ctx, database)
	if err != nil {
		return err
	}
	if !equalCounts(counts, manifest.TargetCounts) {
		return fmt.Errorf("final row counts changed: vacuum=%v staging=%v", counts, manifest.TargetCounts)
	}
	stats, err := readAIStats(ctx, database)
	if err != nil {
		return err
	}
	if stats != manifest.TargetAI {
		return fmt.Errorf("final AI counts or tokens changed: vacuum=%+v staging=%+v", stats, manifest.TargetAI)
	}
	return nil
}

func equalCounts(left, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func validatePreservedCounts(source, target map[string]int64) error {
	for table, targetCount := range target {
		if sourceCount, existed := source[table]; existed && sourceCount != targetCount {
			return fmt.Errorf("preserved table %s row count changed: source=%d target=%d", table, sourceCount, targetCount)
		}
	}
	return nil
}

func readAIStats(ctx context.Context, database *sql.DB) (AIStats, error) {
	row, err := aiq.New(database).GetAIMigrationStats(ctx)
	if err != nil {
		return AIStats{}, err
	}
	return AIStats{
		Sessions: row.Sessions, Messages: row.Messages, SessionMessages: row.SessionMessages,
		Runs: row.Runs, Prompts: row.Prompts, ChatSettings: row.ChatSettings,
		MediaObjects: row.MediaObjects, MediaReferences: row.MediaReferences,
		AssistantPayloads: row.AssistantPayloads, InputTokens: row.InputTokens,
		OutputTokens: row.OutputTokens, CachedInputTokens: row.CachedInputTokens,
	}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeManifest(path string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".main-db-manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
