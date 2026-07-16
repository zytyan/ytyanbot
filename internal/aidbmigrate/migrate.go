package aidbmigrate

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
	"main/helpers/aimedia"
	"main/internal/aidbmigrate/legacyq"
	aischema "main/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type Config struct {
	Source          string
	Output          string
	MediaPath       string
	ManifestPath    string
	DefaultProvider string
	DefaultModel    string
	SetMediaOwner   bool
	MediaUID        int
	MediaGID        int
}

type Counts struct {
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

type Manifest struct {
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
	SourcePath       string    `json:"source_path"`
	OutputPath       string    `json:"output_path"`
	MediaPath        string    `json:"media_path"`
	SourceSHA256     string    `json:"source_sha256"`
	OutputSHA256     string    `json:"output_sha256"`
	MediaListSHA256  string    `json:"media_list_sha256"`
	MediaBytes       int64     `json:"media_bytes"`
	Source           Counts    `json:"source"`
	Target           Counts    `json:"target"`
	UnknownTokenRuns int64     `json:"unknown_token_runs"`
	OrphanModelRuns  int64     `json:"orphan_model_runs"`
	IntegrityCheck   string    `json:"integrity_check"`
	ForeignKeyIssues int64     `json:"foreign_key_issues"`
	TokenCheck       bool      `json:"token_check"`
	SourceUnchanged  bool      `json:"source_unchanged"`
}

type providerState struct {
	GeminiInteractionID    string `json:"gemini_interaction_id,omitempty"`
	WindowStartMsgID       int64  `json:"window_start_msg_id,omitempty"`
	GeminiCacheName        string `json:"gemini_cache_name,omitempty"`
	GeminiCacheExpireTime  int64  `json:"gemini_cache_expire_time,omitempty"`
	GeminiCacheStartMsgID  int64  `json:"gemini_cache_start_msg_id,omitempty"`
	GeminiCacheEndMsgID    int64  `json:"gemini_cache_end_msg_id,omitempty"`
	GeminiCacheTokenCount  int64  `json:"gemini_cache_token_count,omitempty"`
	GeminiCacheFingerprint string `json:"gemini_cache_fingerprint,omitempty"`
	HistoryRebuildLossy    bool   `json:"history_rebuild_lossy,omitempty"`
}

func normalizeConfig(cfg Config) (Config, error) {
	var err error
	if cfg.Source == "" || cfg.Output == "" || cfg.MediaPath == "" {
		return cfg, errors.New("source, output, and media path are required")
	}
	if cfg.DefaultProvider == "" || cfg.DefaultModel == "" {
		return cfg, errors.New("default provider and model are required")
	}
	if cfg.Source, err = filepath.Abs(cfg.Source); err != nil {
		return cfg, err
	}
	if cfg.Output, err = filepath.Abs(cfg.Output); err != nil {
		return cfg, err
	}
	if cfg.MediaPath, err = filepath.Abs(cfg.MediaPath); err != nil {
		return cfg, err
	}
	if cfg.ManifestPath == "" {
		cfg.ManifestPath = cfg.Output + ".manifest.json"
	}
	if cfg.ManifestPath, err = filepath.Abs(cfg.ManifestPath); err != nil {
		return cfg, err
	}
	if cfg.Source == cfg.Output {
		return cfg, errors.New("output must differ from source")
	}
	return cfg, nil
}

func ensureAbsent(paths ...string) error {
	for _, target := range paths {
		_, err := os.Lstat(target)
		if err == nil {
			return fmt.Errorf("target already exists: %s", target)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
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

func sqliteReadOnlyDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	query.Set("_foreign_keys", "on")
	query.Set("_busy_timeout", "5000")
	u.RawQuery = query.Encode()
	return u.String()
}

func sqliteReadWriteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("_foreign_keys", "on")
	query.Set("_busy_timeout", "5000")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	u.RawQuery = query.Encode()
	return u.String()
}

func backupSQLite(ctx context.Context, source *sql.DB, destination string) error {
	destinationDB, err := sql.Open("sqlite3", destination)
	if err != nil {
		return err
	}
	defer destinationDB.Close()
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()
	destinationConn, err := destinationDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()
	return destinationConn.Raw(func(destinationDriver any) error {
		destinationSQLite, ok := destinationDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected destination SQLite connection")
		}
		return sourceConn.Raw(func(sourceDriver any) error {
			sourceSQLite, ok := sourceDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected source SQLite connection")
			}
			backup, err := destinationSQLite.Backup("main", sourceSQLite, "main")
			if err != nil {
				return err
			}
			for {
				if err = ctx.Err(); err != nil {
					_ = backup.Finish()
					return err
				}
				done, stepErr := backup.Step(256)
				if stepErr != nil {
					_ = backup.Finish()
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	})
}

func providerForLegacyModel(model, fallback string) string {
	lower := strings.ToLower(model)
	if strings.HasPrefix(lower, "deepseek") {
		return "deepseek"
	}
	return fallback
}

func normalizedRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "user", nil
	case "model", "assistant":
		return "model", nil
	case "system":
		return "system", nil
	default:
		return "", fmt.Errorf("unsupported legacy role %q", role)
	}
}

func normalizedMessageType(message legacyq.ListLegacyMessagesRow) string {
	if value := strings.TrimSpace(message.MsgType); value != "" {
		return value
	}
	if len(message.Blob) == 0 {
		return "text"
	}
	mimeType := strings.ToLower(message.MimeType.String)
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "photo"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	default:
		return "media"
	}
}

func nullStringValue(value sql.NullString, fallback string) string {
	if value.Valid && value.String != "" {
		return value.String
	}
	return fallback
}

func intFlag(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func migrateStaging(ctx context.Context, database *sql.DB, store *aimedia.Store, cfg Config,
	manifest *Manifest, migrationTime int64,
) (err error) {
	legacy := legacyq.New(database)
	settings, err := legacy.ListLegacyChatSettings(ctx)
	if err != nil {
		return fmt.Errorf("read legacy chat settings: %w", err)
	}
	sessions, err := legacy.ListLegacySessions(ctx)
	if err != nil {
		return fmt.Errorf("read legacy sessions: %w", err)
	}
	messages, err := legacy.ListLegacyMessages(ctx)
	if err != nil {
		return fmt.Errorf("read legacy messages: %w", err)
	}
	v0Messages, err := legacy.ListLegacyV0Messages(ctx)
	if err != nil {
		return fmt.Errorf("read legacy v0 messages: %w", err)
	}
	for _, message := range v0Messages {
		messages = append(messages, legacyq.ListLegacyMessagesRow{
			SessionID: message.SessionID, ChatID: message.ChatID, MsgID: message.MsgID,
			Role: message.Role, SentTime: message.SentTime, MsgType: "text",
			ReplyToMsgID: message.ReplyToMsgID,
			Text:         sql.NullString{String: message.Text, Valid: true},
			UserID:       message.UserID,
		})
	}
	sort.SliceStable(messages, func(left, right int) bool {
		if messages[left].SessionID != messages[right].SessionID {
			return messages[left].SessionID < messages[right].SessionID
		}
		if messages[left].SentTime != messages[right].SentTime {
			return messages[left].SentTime < messages[right].SentTime
		}
		return messages[left].MsgID < messages[right].MsgID
	})
	prompts, err := legacy.ListLegacySystemPrompts(ctx)
	if err != nil {
		return fmt.Errorf("read legacy prompts: %w", err)
	}
	manifest.Source.ChatSettings = int64(len(settings))
	manifest.Source.Sessions = int64(len(sessions))
	manifest.Source.Messages = int64(len(messages))
	manifest.Source.SessionMessages = int64(len(messages))
	manifest.Source.Prompts = int64(len(prompts))

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, aischema.V2); err != nil {
		return fmt.Errorf("create AI V2 schema: %w", err)
	}
	target := aiq.New(tx)

	for _, setting := range settings {
		provider := providerForLegacyModel(setting.Model, cfg.DefaultProvider)
		showUsage := setting.ShowUsage
		if showUsage != 0 {
			showUsage = 1
		}
		if _, err = target.UpsertAIChatSettings(ctx, aiq.UpsertAIChatSettingsParams{
			ChatID: setting.ChatID, DefaultProvider: provider, DefaultModel: setting.Model,
			ShowUsage: showUsage, UpdatedAt: migrationTime,
		}); err != nil {
			return err
		}
	}

	sessionModels := make(map[int64][2]string, len(sessions))
	for _, session := range sessions {
		provider := nullStringValue(session.Provider, cfg.DefaultProvider)
		model := nullStringValue(session.Model, cfg.DefaultModel)
		if !session.Provider.Valid && session.Model.Valid {
			provider = providerForLegacyModel(model, cfg.DefaultProvider)
		}
		createdAt, updatedAt := session.FirstMessageAt, session.LastMessageAt
		if createdAt == 0 {
			createdAt = migrationTime
		}
		if updatedAt == 0 {
			updatedAt = migrationTime
		}
		status := "active"
		if session.Frozen != 0 {
			status = "frozen"
		}
		cachedTokens := session.CachedInputTokens.Int64
		if cachedTokens < 0 || session.TotalInputTokens < 0 || session.TotalOutputTokens < 0 {
			return fmt.Errorf("session %d has negative token totals", session.ID)
		}
		if err = target.CreateMigratedAISession(ctx, aiq.CreateMigratedAISessionParams{
			ID: session.ID, ChatID: session.ChatID, ChatName: session.ChatName,
			ChatType: session.ChatType, Status: status, Provider: provider, Model: model,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
			TotalInputTokens: session.TotalInputTokens, TotalOutputTokens: session.TotalOutputTokens,
			TotalCachedInputTokens: cachedTokens,
			HistoryRebuildLossy:    intFlag(session.HistoryRebuildLossy.Valid && session.HistoryRebuildLossy.Int64 != 0),
		}); err != nil {
			return err
		}
		manifest.Source.InputTokens += session.TotalInputTokens
		manifest.Source.OutputTokens += session.TotalOutputTokens
		manifest.Source.CachedInputTokens += cachedTokens
		sessionModels[session.ID] = [2]string{provider, model}

		if session.Provider.Valid {
			state := providerState{
				GeminiInteractionID:    session.GeminiInteractionID.String,
				WindowStartMsgID:       session.WindowStartMsgID.Int64,
				GeminiCacheName:        session.GeminiCacheName.String,
				GeminiCacheExpireTime:  session.GeminiCacheExpireTime.Int64,
				GeminiCacheStartMsgID:  session.GeminiCacheStartMsgID.Int64,
				GeminiCacheEndMsgID:    session.GeminiCacheEndMsgID.Int64,
				GeminiCacheTokenCount:  session.GeminiCacheTokenCount.Int64,
				GeminiCacheFingerprint: session.GeminiCacheFingerprint.String,
				HistoryRebuildLossy:    session.HistoryRebuildLossy.Valid && session.HistoryRebuildLossy.Int64 != 0,
			}
			stateJSON, marshalErr := json.Marshal(state)
			if marshalErr != nil {
				return marshalErr
			}
			if err = target.UpsertAISessionProviderState(ctx, aiq.UpsertAISessionProviderStateParams{
				SessionID: session.ID, Provider: provider, StateVersion: 1,
				StateJson: string(stateJSON), UpdatedAt: updatedAt,
			}); err != nil {
				return err
			}
		}
	}

	type requestRef struct {
		chatID, msgID, sentAt int64
	}
	positions := make(map[int64]int64, len(sessions))
	lastUsers := make(map[int64]requestRef, len(sessions))
	usedRequests := make(map[string]struct{})
	mediaHashes := make(map[string]int64)
	for _, message := range messages {
		role, roleErr := normalizedRole(message.Role)
		if roleErr != nil {
			return roleErr
		}
		if _, ok := sessionModels[message.SessionID]; !ok {
			return fmt.Errorf("message %d references missing session %d", message.MsgID, message.SessionID)
		}
		if err = target.InsertAIMessage(ctx, aiq.InsertAIMessageParams{
			ChatID: message.ChatID, MsgID: message.MsgID, SentAt: message.SentTime,
			UserID: message.UserID, Username: message.Username, AtableUsername: message.AtableUsername,
			MsgType: normalizedMessageType(message), Text: message.Text, ReplyToMsgID: message.ReplyToMsgID,
		}); err != nil {
			return err
		}
		position := positions[message.SessionID]
		if err = target.AddAISessionMessage(ctx, aiq.AddAISessionMessageParams{
			SessionID: message.SessionID, Position: position, ChatID: message.ChatID,
			MsgID: message.MsgID, Role: role, QuotePart: message.QuotePart,
		}); err != nil {
			return err
		}
		positions[message.SessionID] = position + 1

		if len(message.Blob) > 0 {
			if !message.MimeType.Valid || message.MimeType.String == "" {
				return fmt.Errorf("message %d has media without MIME type", message.MsgID)
			}
			object, putErr := store.Put(message.Blob)
			if putErr != nil {
				return putErr
			}
			if err = target.InsertMediaObject(ctx, aiq.InsertMediaObjectParams{
				Sha256: object.SHA256, RelativePath: object.RelativePath, ByteSize: object.Size,
				MimeType: message.MimeType.String, CreatedAt: migrationTime,
			}); err != nil {
				return err
			}
			if err = target.AddAIMessageMedia(ctx, aiq.AddAIMessageMediaParams{
				ChatID: message.ChatID, MsgID: message.MsgID, Ordinal: 0,
				MediaSha256: object.SHA256, MediaKind: normalizedMessageType(message),
			}); err != nil {
				return err
			}
			manifest.Source.MediaReferences++
			mediaHashes[object.SHA256] = object.Size
		}

		if role == "user" {
			lastUsers[message.SessionID] = requestRef{message.ChatID, message.MsgID, message.SentTime}
			continue
		}
		if role != "model" {
			continue
		}
		if len(message.AssistantPayload) > 0 {
			manifest.Source.AssistantPayloads++
		}
		request, hasUser := lastUsers[message.SessionID]
		requestKey := fmt.Sprintf("%d:%d:%d", message.SessionID, request.chatID, request.msgID)
		if !hasUser {
			request = requestRef{message.ChatID, message.MsgID, message.SentTime}
			manifest.OrphanModelRuns++
			requestKey = fmt.Sprintf("%d:%d:%d", message.SessionID, request.chatID, request.msgID)
		}
		if _, used := usedRequests[requestKey]; used {
			request = requestRef{message.ChatID, message.MsgID, message.SentTime}
			manifest.OrphanModelRuns++
			requestKey = fmt.Sprintf("%d:%d:%d", message.SessionID, request.chatID, request.msgID)
		}
		usedRequests[requestKey] = struct{}{}
		providerModel := sessionModels[message.SessionID]
		provider := nullStringValue(message.ResponseProvider, providerModel[0])
		model := nullStringValue(message.ResponseModel, providerModel[1])
		run, createErr := target.CreateAIRun(ctx, aiq.CreateAIRunParams{
			SessionID: message.SessionID, RequestChatID: request.chatID, RequestMsgID: request.msgID,
			Provider: provider, Model: model, RequestedAt: request.sentAt,
		})
		if createErr != nil {
			return createErr
		}
		hasTokenMetadata := message.InputTokens.Int64 > 0 || message.OutputTokens.Int64 > 0 ||
			message.CachedInputTokens.Int64 > 0 || message.InputMessageCount.Int64 > 0
		inputTokens, outputTokens, cachedTokens := message.InputTokens, message.OutputTokens, message.CachedInputTokens
		messageCount := message.InputMessageCount
		firstMessageID, lastMessageID := message.InputFirstMsgID, message.InputLastMsgID
		if !hasTokenMetadata {
			manifest.UnknownTokenRuns++
			inputTokens, outputTokens, cachedTokens = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
			messageCount, firstMessageID, lastMessageID = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
		}
		rows, markErr := target.MarkAIRunGenerated(ctx, aiq.MarkAIRunGeneratedParams{
			CompletedAt: sql.NullInt64{Int64: message.SentTime, Valid: true},
			InputTokens: inputTokens, OutputTokens: outputTokens, CachedInputTokens: cachedTokens,
			InputMessageCount: messageCount,
			InputFirstMsgID:   firstMessageID, InputLastMsgID: lastMessageID,
			ResponseText: message.Text, ThoughtSignature: message.ThoughtSignature,
			AssistantPayload:       message.AssistantPayload,
			AssistantPayloadFormat: message.AssistantPayloadFormat,
			RunID:                  run.ID,
		})
		if markErr != nil {
			return markErr
		}
		if rows != 1 {
			return fmt.Errorf("run %d did not transition to generated", run.ID)
		}
		rows, markErr = target.MarkAIRunDelivered(ctx,
			sql.NullInt64{Int64: message.ChatID, Valid: true},
			sql.NullInt64{Int64: message.MsgID, Valid: true}, run.ID)
		if markErr != nil {
			return markErr
		}
		if rows != 1 {
			return fmt.Errorf("run %d did not transition to delivered", run.ID)
		}
		manifest.Source.Runs++
	}

	manifest.Source.MediaObjects = int64(len(mediaHashes))
	for _, prompt := range prompts {
		cleaned := strings.ReplaceAll(prompt.Prompt, "%MEMORIES%", "")
		if err = target.UpsertAISystemPrompt(ctx, prompt.ChatID, prompt.ThreadID, cleaned, migrationTime); err != nil {
			return err
		}
	}
	for _, definition := range migrationdefs.All {
		if definition.Version > migrationdefs.AIV2Version {
			break
		}
		if err = target.RecordSchemaMigration(ctx, definition.Version, definition.Name,
			migrationdefs.Checksum(definition.Source), migrationTime); err != nil {
			return err
		}
	}
	if err = dropLegacyAITables(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	stats, err := aiq.New(database).GetAIMigrationStats(ctx)
	if err != nil {
		return err
	}
	manifest.Target = Counts{
		Sessions: stats.Sessions, Messages: stats.Messages, SessionMessages: stats.SessionMessages,
		Runs: stats.Runs, Prompts: stats.Prompts, ChatSettings: stats.ChatSettings,
		MediaObjects: stats.MediaObjects, MediaReferences: stats.MediaReferences,
		AssistantPayloads: stats.AssistantPayloads,
		InputTokens:       stats.InputTokens, OutputTokens: stats.OutputTokens,
		CachedInputTokens: stats.CachedInputTokens,
	}
	manifest.TokenCheck = manifest.Source.InputTokens == manifest.Target.InputTokens &&
		manifest.Source.OutputTokens == manifest.Target.OutputTokens &&
		manifest.Source.CachedInputTokens == manifest.Target.CachedInputTokens
	if !manifest.TokenCheck || manifest.Source.Sessions != manifest.Target.Sessions ||
		manifest.Source.Messages != manifest.Target.Messages ||
		manifest.Source.SessionMessages != manifest.Target.SessionMessages ||
		manifest.Source.Runs != manifest.Target.Runs ||
		manifest.Source.Prompts != manifest.Target.Prompts ||
		manifest.Source.ChatSettings != manifest.Target.ChatSettings ||
		manifest.Source.MediaObjects != manifest.Target.MediaObjects ||
		manifest.Source.MediaReferences != manifest.Target.MediaReferences ||
		manifest.Source.AssistantPayloads != manifest.Target.AssistantPayloads {
		return fmt.Errorf("migration count or token validation failed: source=%+v target=%+v",
			manifest.Source, manifest.Target)
	}
	return nil
}

func dropLegacyAITables(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
DROP TABLE IF EXISTS ai_message_meta;
DROP TABLE IF EXISTS ai_session_meta;
DROP TABLE IF EXISTS ai_chat_models;
DROP TABLE IF EXISTS gemini_system_prompt;
DROP TABLE IF EXISTS gemini_memories;
DROP TABLE IF EXISTS gemini_session_migrations;
DROP TABLE IF EXISTS gemini_messages;
DROP TABLE IF EXISTS gemini_contents;
DROP TABLE IF EXISTS gemini_sessions;`)
	return err
}

func validateDatabase(ctx context.Context, databasePath string, store *aimedia.Store,
	manifest *Manifest,
) error {
	database, err := sql.Open("sqlite3", sqliteReadOnlyDSN(databasePath))
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
		var table, parent string
		var rowID any
		var foreignKeyID int64
		if err = rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			_ = rows.Close()
			return err
		}
		manifest.ForeignKeyIssues++
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if manifest.ForeignKeyIssues != 0 {
		return fmt.Errorf("foreign_key_check returned %d rows", manifest.ForeignKeyIssues)
	}
	legacyNames := []string{
		"gemini_sessions", "gemini_contents", "gemini_system_prompt", "gemini_memories",
		"gemini_messages", "gemini_session_migrations", "ai_chat_models", "ai_session_meta",
		"ai_message_meta",
	}
	for _, table := range legacyNames {
		var exists bool
		if err = database.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("legacy AI table remains: %s", table)
		}
	}
	queries := aiq.New(database)
	hashes, err := queries.ListReferencedMediaHashes(ctx)
	if err != nil {
		return err
	}
	var list strings.Builder
	var mediaBytes int64
	for _, hash := range hashes {
		object, err := queries.GetMediaObject(ctx, hash)
		if err != nil {
			return err
		}
		if err = store.Verify(hash, object.ByteSize); err != nil {
			return err
		}
		mediaBytes += object.ByteSize
		_, _ = fmt.Fprintf(&list, "%s\t%d\t%s\t%s\n",
			hash, object.ByteSize, object.MimeType, object.RelativePath)
	}
	digest := sha256.Sum256([]byte(list.String()))
	manifest.MediaListSHA256 = hex.EncodeToString(digest[:])
	manifest.MediaBytes = mediaBytes
	stats, err := queries.GetAIMigrationStats(ctx)
	if err != nil {
		return err
	}
	actual := Counts{
		Sessions: stats.Sessions, Messages: stats.Messages, SessionMessages: stats.SessionMessages,
		Runs: stats.Runs, Prompts: stats.Prompts, ChatSettings: stats.ChatSettings,
		MediaObjects: stats.MediaObjects, MediaReferences: stats.MediaReferences,
		AssistantPayloads: stats.AssistantPayloads,
		InputTokens:       stats.InputTokens, OutputTokens: stats.OutputTokens,
		CachedInputTokens: stats.CachedInputTokens,
	}
	if actual != manifest.Target {
		return fmt.Errorf("final database stats changed: migrated=%+v final=%+v", manifest.Target, actual)
	}
	return nil
}

func setMediaOwnership(root string, uid, gid int) error {
	if uid < 0 && gid < 0 {
		return nil
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0640)
		if info.IsDir() {
			mode = 0750
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	})
}

func writeManifestFile(path string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ai-migration-manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := true
	defer func() {
		_ = temporary.Close()
		if keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = false
	return nil
}

func Run(ctx context.Context, input Config) (manifest Manifest, err error) {
	cfg, err := normalizeConfig(input)
	if err != nil {
		return manifest, err
	}
	if err = ensureAbsent(cfg.Output, cfg.MediaPath, cfg.ManifestPath); err != nil {
		return manifest, err
	}
	for _, directory := range []string{
		filepath.Dir(cfg.Output), filepath.Dir(cfg.MediaPath), filepath.Dir(cfg.ManifestPath),
	} {
		if err = os.MkdirAll(directory, 0750); err != nil {
			return manifest, err
		}
	}
	manifest = Manifest{
		StartedAt: time.Now().UTC(), SourcePath: cfg.Source, OutputPath: cfg.Output,
		MediaPath: cfg.MediaPath,
	}
	if manifest.SourceSHA256, err = fileSHA256(cfg.Source); err != nil {
		return manifest, err
	}

	workDirectory, err := os.MkdirTemp(filepath.Dir(cfg.Output), ".ai-db-migrate-*")
	if err != nil {
		return manifest, err
	}
	defer os.RemoveAll(workDirectory)
	mediaTemporary, err := os.MkdirTemp(filepath.Dir(cfg.MediaPath), ".ai-media-migrate-*")
	if err != nil {
		return manifest, err
	}
	keepMediaTemporary := true
	defer func() {
		if keepMediaTemporary {
			_ = os.RemoveAll(mediaTemporary)
		}
	}()
	if err = os.Chmod(mediaTemporary, 0750); err != nil {
		return manifest, err
	}
	store, err := aimedia.NewStore(mediaTemporary)
	if err != nil {
		return manifest, err
	}

	source, err := sql.Open("sqlite3", sqliteReadOnlyDSN(cfg.Source))
	if err != nil {
		return manifest, err
	}
	stagingPath := filepath.Join(workDirectory, "staging.db")
	if err = backupSQLite(ctx, source, stagingPath); err != nil {
		_ = source.Close()
		return manifest, fmt.Errorf("backup source database: %w", err)
	}
	if err = source.Close(); err != nil {
		return manifest, err
	}

	staging, err := sql.Open("sqlite3", sqliteReadWriteDSN(stagingPath))
	if err != nil {
		return manifest, err
	}
	staging.SetMaxOpenConns(1)
	migrationTime := time.Now().Unix()
	if err = migrateStaging(ctx, staging, store, cfg, &manifest, migrationTime); err != nil {
		_ = staging.Close()
		return manifest, fmt.Errorf("migrate staging database: %w", err)
	}
	if _, err = staging.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = staging.Close()
		return manifest, err
	}
	finalTemporary := filepath.Join(workDirectory, "final.db")
	if _, err = staging.ExecContext(ctx, `VACUUM INTO ?`, finalTemporary); err != nil {
		_ = staging.Close()
		return manifest, fmt.Errorf("vacuum final database: %w", err)
	}
	if err = staging.Close(); err != nil {
		return manifest, err
	}
	if err = os.Chmod(finalTemporary, 0600); err != nil {
		return manifest, err
	}
	if err = validateDatabase(ctx, finalTemporary, store, &manifest); err != nil {
		return manifest, fmt.Errorf("validate final database: %w", err)
	}
	if manifest.OutputSHA256, err = fileSHA256(finalTemporary); err != nil {
		return manifest, err
	}
	afterSourceHash, err := fileSHA256(cfg.Source)
	if err != nil {
		return manifest, err
	}
	manifest.SourceUnchanged = afterSourceHash == manifest.SourceSHA256
	if !manifest.SourceUnchanged {
		return manifest, errors.New("source database file changed during migration")
	}
	if cfg.SetMediaOwner {
		if err = setMediaOwnership(mediaTemporary, cfg.MediaUID, cfg.MediaGID); err != nil {
			return manifest, err
		}
	}
	manifest.CompletedAt = time.Now().UTC()

	if err = os.Rename(finalTemporary, cfg.Output); err != nil {
		return manifest, err
	}
	outputPublished := true
	defer func() {
		if err != nil && outputPublished {
			_ = os.Remove(cfg.Output)
		}
	}()
	if err = os.Rename(mediaTemporary, cfg.MediaPath); err != nil {
		return manifest, err
	}
	keepMediaTemporary = false
	mediaPublished := true
	defer func() {
		if err != nil && mediaPublished {
			_ = os.RemoveAll(cfg.MediaPath)
		}
	}()
	if err = writeManifestFile(cfg.ManifestPath, manifest); err != nil {
		return manifest, err
	}
	outputPublished = false
	mediaPublished = false
	return manifest, nil
}
