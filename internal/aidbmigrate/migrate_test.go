package aidbmigrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"main/globalcfg/aiq"
	"main/globalcfg/migrationdefs"
	"main/helpers/aimedia"
	legacyschema "main/sql/migrate"

	_ "github.com/mattn/go-sqlite3"
)

func createLegacyFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer database.Close()
	if _, err = database.Exec(legacyschema.V1); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	_, err = database.Exec(`
CREATE TABLE non_ai_data(id INTEGER PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE gemini_memories(id INTEGER PRIMARY KEY, chat_id INTEGER, topic_id INTEGER, content TEXT);
INSERT INTO non_ai_data(id, value) VALUES (1, 'preserve me');
INSERT INTO gemini_memories(id, chat_id, topic_id, content) VALUES (1, -100, 0, 'discard me');

INSERT INTO gemini_sessions(id, chat_id, chat_name, chat_type, frozen, total_input_tokens, total_output_tokens)
VALUES (10, -100, 'chat', 'supergroup', 0, 100, 20),
       (20, -200, 'empty', 'private', 1, 0, 0);
INSERT INTO gemini_messages(session_id, chat_id, tg_message_id, from_id, role, content,
  seq, reply_to_seq, created_at)
VALUES (10, -100, 90, 7, 'user', 'v0 question', 1, NULL, 900),
       (10, -100, 91, 999, 'model', 'v0 answer', 2, 1, 901),
       (10, -100, 103, 999, 'model', 'overlap ignored', 3, 2, 1003);
INSERT INTO ai_session_meta(session_id, provider, model, cached_input_tokens,
  gemini_interaction_id, window_start_msg_id, gemini_cache_name, gemini_cache_expire_time,
  gemini_cache_start_msg_id, gemini_cache_end_msg_id, gemini_cache_token_count,
  gemini_cache_fingerprint, history_rebuild_lossy)
VALUES (10, 'gemini', 'gemini-old', 50, 'interaction-10', 100,
  'cachedContents/10', 1800000000, 100, 102, 5000, 'fingerprint', 0);
INSERT INTO ai_chat_models(chat_id, model, show_usage)
VALUES (-100, 'deepseek-v4-flash', 1);
INSERT INTO gemini_system_prompt(chat_id, thread_id, prompt)
VALUES (-100, 9, 'hello %MEMORIES% world');

INSERT INTO gemini_contents(session_id, chat_id, msg_id, role, sent_time, username,
  msg_type, text, blob, mime_type, quote_part, thought_signature, atable_username, user_id)
VALUES
  (10, -100, 100, 'user', 1000, 'Alice', 'text', 'question', NULL, NULL, NULL, NULL, 'alice', 7),
  (10, -100, 101, 'model', 1001, 'Bot', 'text', 'answer', NULL, NULL, NULL, 'signature', 'bot', 999),
  (10, -100, 102, 'user', 1002, 'Alice', '', 'caption', x'6d656469612d6279746573', 'image/jpeg', NULL, NULL, 'alice', 7),
  (10, -100, 103, 'model', 1003, 'Bot', 'text', 'second answer', NULL, NULL, NULL, NULL, 'bot', 999);
INSERT INTO ai_message_meta(session_id, msg_id, chat_id, provider, model,
  input_tokens, output_tokens, cached_input_tokens, input_message_count,
  input_first_msg_id, input_last_msg_id, assistant_payload, assistant_payload_format)
VALUES
  (10, 101, -100, 'gemini', 'gemini-old', 10, 2, 5, 1, 100, 100,
   x'7b22726f6c65223a226d6f64656c227d', 'gemini-content-v1'),
  (10, 103, -100, 'gemini', 'gemini-old', 0, 0, 0, 0, 0, 0,
   x'7b22726f6c65223a226d6f64656c227d', 'gemini-content-v1');`)
	if err != nil {
		t.Fatalf("populate legacy fixture: %v", err)
	}
	return path
}

func TestOfflineMigrationPreservesDataAndRemovesLegacyAI(t *testing.T) {
	ctx := context.Background()
	source := createLegacyFixture(t)
	root := t.TempDir()
	output := filepath.Join(root, "v2.db")
	mediaPath := filepath.Join(root, "ai-media")
	manifestPath := filepath.Join(root, "migration.json")
	manifest, err := Run(ctx, Config{
		Source: source, Output: output, MediaPath: mediaPath, ManifestPath: manifestPath,
		DefaultProvider: "gemini", DefaultModel: "gemini-3-flash-preview",
		MediaUID: -1, MediaGID: -1,
	})
	if err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if manifest.IntegrityCheck != "ok" || manifest.ForeignKeyIssues != 0 || !manifest.TokenCheck {
		t.Fatalf("invalid migration checks: %+v", manifest)
	}
	if !manifest.SourceUnchanged || manifest.SourceSHA256 == "" || manifest.OutputSHA256 == "" {
		t.Fatalf("missing source/output checksums: %+v", manifest)
	}
	if manifest.Source.Sessions != 2 || manifest.Target.Sessions != 2 ||
		manifest.Source.Messages != 6 || manifest.Target.Messages != 6 ||
		manifest.Target.Runs != 3 || manifest.Target.MediaObjects != 1 ||
		manifest.Target.MediaReferences != 1 || manifest.Target.AssistantPayloads != 2 ||
		manifest.UnknownTokenRuns != 2 {
		t.Fatalf("unexpected counts: source=%+v target=%+v unknown=%d",
			manifest.Source, manifest.Target, manifest.UnknownTokenRuns)
	}
	if _, err = os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	database, err := sql.Open("sqlite3", sqliteReadOnlyDSN(output))
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer database.Close()
	queries := aiq.New(database)
	session, err := queries.GetAISession(ctx, 10)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.CreatedAt != 900 || session.UpdatedAt != 1003 || session.TopicID.Valid {
		t.Fatalf("session time/topic migration mismatch: %+v", session)
	}
	emptySession, err := queries.GetAISession(ctx, 20)
	if err != nil || emptySession.CreatedAt == 0 || emptySession.UpdatedAt == 0 || emptySession.Status != "frozen" {
		t.Fatalf("empty session migration mismatch: %+v err=%v", emptySession, err)
	}
	settings, err := queries.GetAIChatSettings(ctx, -100)
	if err != nil || settings.DefaultProvider != "deepseek" || settings.ShowUsage != 1 {
		t.Fatalf("settings migration mismatch: %+v err=%v", settings, err)
	}
	prompt, err := queries.GetAISystemPrompt(ctx, -100, 9)
	if err != nil || prompt != "hello  world" {
		t.Fatalf("prompt migration mismatch: %q err=%v", prompt, err)
	}
	knownRun, err := queries.GetAIRunByResponse(ctx,
		sql.NullInt64{Int64: -100, Valid: true}, sql.NullInt64{Int64: 101, Valid: true})
	if err != nil || knownRun.Status != "delivered" || knownRun.InputTokens.Int64 != 10 ||
		string(knownRun.AssistantPayload) != `{"role":"model"}` {
		t.Fatalf("known run migration mismatch: %+v err=%v", knownRun, err)
	}
	unknownRun, err := queries.GetAIRunByResponse(ctx,
		sql.NullInt64{Int64: -100, Valid: true}, sql.NullInt64{Int64: 103, Valid: true})
	if err != nil || unknownRun.InputTokens.Valid || unknownRun.CacheExpireAt.Valid {
		t.Fatalf("unknown usage/cache must remain NULL: %+v err=%v", unknownRun, err)
	}
	state, err := queries.GetAISessionProviderState(ctx, 10)
	if err != nil || state.Provider != "gemini" || state.StateVersion != 1 {
		t.Fatalf("provider state migration mismatch: %+v err=%v", state, err)
	}
	var nonAI string
	if err = database.QueryRowContext(ctx, `SELECT value FROM non_ai_data WHERE id=1`).Scan(&nonAI); err != nil || nonAI != "preserve me" {
		t.Fatalf("non-AI data not preserved: %q err=%v", nonAI, err)
	}
	for _, table := range []string{"gemini_sessions", "gemini_contents", "gemini_system_prompt",
		"gemini_memories", "gemini_messages", "gemini_session_migrations",
		"ai_chat_models", "ai_session_meta", "ai_message_meta"} {
		var exists bool
		if err = database.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil || exists {
			t.Fatalf("legacy table %s remains: exists=%v err=%v", table, exists, err)
		}
	}
	var migrationCount int
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil || migrationCount != int(migrationdefs.AIV2Version) {
		t.Fatalf("migration history mismatch: count=%d err=%v", migrationCount, err)
	}
	hashes, err := queries.ListReferencedMediaHashes(ctx)
	if err != nil || len(hashes) != 1 {
		t.Fatalf("media references mismatch: %v err=%v", hashes, err)
	}
	object, err := queries.GetMediaObject(ctx, hashes[0])
	if err != nil {
		t.Fatalf("get media object: %v", err)
	}
	store, err := aimedia.NewStore(mediaPath)
	if err != nil {
		t.Fatalf("open media store: %v", err)
	}
	if err = store.Verify(object.Sha256, object.ByteSize); err != nil {
		t.Fatalf("verify migrated media: %v", err)
	}

	sourceDB, err := sql.Open("sqlite3", sqliteReadOnlyDSN(source))
	if err != nil {
		t.Fatalf("reopen source: %v", err)
	}
	defer sourceDB.Close()
	var sourceMemories int
	if err = sourceDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM gemini_memories`).Scan(&sourceMemories); err != nil || sourceMemories != 1 {
		t.Fatalf("source database was modified: memories=%d err=%v", sourceMemories, err)
	}
}

func TestOfflineMigrationRefusesExistingTargets(t *testing.T) {
	source := createLegacyFixture(t)
	root := t.TempDir()
	output := filepath.Join(root, "exists.db")
	if err := os.WriteFile(output, []byte("occupied"), 0600); err != nil {
		t.Fatalf("create occupied target: %v", err)
	}
	_, err := Run(context.Background(), Config{
		Source: source, Output: output, MediaPath: filepath.Join(root, "media"),
		DefaultProvider: "gemini", DefaultModel: "gemini-test", MediaUID: -1, MediaGID: -1,
	})
	if err == nil {
		t.Fatalf("expected existing output to be rejected")
	}
}
