package legacyarchive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type Config struct {
	MessageDB string
	WALDB     string
	MeiliDump string
	OutputDir string
}

type Artifact struct {
	Name            string           `json:"name"`
	SourcePath      string           `json:"source_path"`
	ArchivePath     string           `json:"archive_path"`
	SourceSHA256    string           `json:"source_sha256"`
	ArchiveSHA256   string           `json:"archive_sha256"`
	SourceUnchanged bool             `json:"source_unchanged"`
	IntegrityCheck  string           `json:"integrity_check,omitempty"`
	TableCounts     map[string]int64 `json:"table_counts,omitempty"`
}

type Manifest struct {
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
	Artifacts   []Artifact `json:"artifacts"`
}

func Run(ctx context.Context, cfg Config) (manifest Manifest, err error) {
	manifest.StartedAt = time.Now().UTC()
	cfg, err = normalize(cfg)
	if err != nil {
		return manifest, err
	}
	if _, err = os.Lstat(cfg.OutputDir); err == nil {
		return manifest, fmt.Errorf("output directory already exists: %s", cfg.OutputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	if err = os.Mkdir(cfg.OutputDir, 0750); err != nil {
		return manifest, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(cfg.OutputDir)
		}
	}()
	messageArtifact, err := archiveSQLite(ctx, "message-db", cfg.MessageDB,
		filepath.Join(cfg.OutputDir, "messages.db"), []string{"saved_msgs", "raw_update", "edit_history"})
	if err != nil {
		return manifest, err
	}
	manifest.Artifacts = append(manifest.Artifacts, messageArtifact)
	walArtifact, err := archiveSQLite(ctx, "meili-wal", cfg.WALDB,
		filepath.Join(cfg.OutputDir, "meili-wal.db"), []string{"meili_wal"})
	if err != nil {
		return manifest, err
	}
	manifest.Artifacts = append(manifest.Artifacts, walArtifact)
	dumpArtifact, err := archiveFile("meili-dump", cfg.MeiliDump, filepath.Join(cfg.OutputDir, filepath.Base(cfg.MeiliDump)))
	if err != nil {
		return manifest, err
	}
	manifest.Artifacts = append(manifest.Artifacts, dumpArtifact)
	manifest.CompletedAt = time.Now().UTC()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	data = append(data, '\n')
	if err = os.WriteFile(filepath.Join(cfg.OutputDir, "manifest.json"), data, 0640); err != nil {
		return manifest, err
	}
	complete = true
	return manifest, nil
}

func normalize(cfg Config) (Config, error) {
	if cfg.MessageDB == "" || cfg.WALDB == "" || cfg.MeiliDump == "" || cfg.OutputDir == "" {
		return cfg, errors.New("message-db, wal-db, meili-dump, and output-dir are required")
	}
	values := []*string{&cfg.MessageDB, &cfg.WALDB, &cfg.MeiliDump, &cfg.OutputDir}
	for _, value := range values {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return cfg, err
		}
		*value = absolute
	}
	return cfg, nil
}

func archiveSQLite(ctx context.Context, name, sourcePath, outputPath string, tables []string) (Artifact, error) {
	artifact := Artifact{Name: name, SourcePath: sourcePath, ArchivePath: outputPath}
	var err error
	artifact.SourceSHA256, err = fileSHA256(sourcePath)
	if err != nil {
		return artifact, err
	}
	source, err := sql.Open("sqlite3", sqliteDSN(sourcePath, "ro"))
	if err != nil {
		return artifact, err
	}
	defer source.Close()
	destination, err := sql.Open("sqlite3", sqliteDSN(outputPath, "rwc"))
	if err != nil {
		return artifact, err
	}
	if err = backupDatabase(ctx, source, destination); err != nil {
		_ = destination.Close()
		return artifact, err
	}
	if err = destination.Close(); err != nil {
		return artifact, err
	}
	archive, err := sql.Open("sqlite3", sqliteDSN(outputPath, "ro"))
	if err != nil {
		return artifact, err
	}
	defer archive.Close()
	if err = archive.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&artifact.IntegrityCheck); err != nil {
		return artifact, err
	}
	if artifact.IntegrityCheck != "ok" {
		return artifact, fmt.Errorf("%s integrity_check: %s", name, artifact.IntegrityCheck)
	}
	artifact.TableCounts = make(map[string]int64, len(tables))
	for _, table := range tables {
		var exists bool
		if err = archive.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return artifact, err
		}
		if !exists {
			return artifact, fmt.Errorf("%s is missing required table %s", name, table)
		}
		var count int64
		if err = archive.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`"`).Scan(&count); err != nil {
			return artifact, err
		}
		artifact.TableCounts[table] = count
	}
	artifact.ArchiveSHA256, err = fileSHA256(outputPath)
	if err != nil {
		return artifact, err
	}
	after, err := fileSHA256(sourcePath)
	if err != nil {
		return artifact, err
	}
	artifact.SourceUnchanged = after == artifact.SourceSHA256
	if !artifact.SourceUnchanged {
		return artifact, fmt.Errorf("%s changed during archive", name)
	}
	return artifact, nil
}

func archiveFile(name, sourcePath, outputPath string) (Artifact, error) {
	artifact := Artifact{Name: name, SourcePath: sourcePath, ArchivePath: outputPath}
	var err error
	artifact.SourceSHA256, err = fileSHA256(sourcePath)
	if err != nil {
		return artifact, err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return artifact, err
	}
	destination, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		_ = source.Close()
		return artifact, err
	}
	_, copyErr := io.Copy(destination, source)
	syncErr := destination.Sync()
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	for _, candidate := range []error{copyErr, syncErr, closeDestinationErr, closeSourceErr} {
		if candidate != nil {
			return artifact, candidate
		}
	}
	artifact.ArchiveSHA256, err = fileSHA256(outputPath)
	if err != nil {
		return artifact, err
	}
	after, err := fileSHA256(sourcePath)
	if err != nil {
		return artifact, err
	}
	artifact.SourceUnchanged = after == artifact.SourceSHA256
	if !artifact.SourceUnchanged || artifact.ArchiveSHA256 != artifact.SourceSHA256 {
		return artifact, fmt.Errorf("%s copy checksum mismatch", name)
	}
	return artifact, nil
}

func sqliteDSN(path, mode string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", mode)
	query.Set("_foreign_keys", "on")
	u.RawQuery = query.Encode()
	return u.String()
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
		destinationSQLite := destinationDriver.(*sqlite3.SQLiteConn)
		return sourceConn.Raw(func(sourceDriver any) error {
			backup, err := destinationSQLite.Backup("main", sourceDriver.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			for {
				done, stepErr := backup.Step(256)
				if stepErr != nil {
					_ = backup.Finish()
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				if err = ctx.Err(); err != nil {
					_ = backup.Finish()
					return err
				}
			}
		})
	})
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
