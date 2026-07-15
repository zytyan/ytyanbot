-- name: UpsertAIChatSettings :one
INSERT INTO ai_chat_settings(chat_id, default_provider, default_model, show_usage, updated_at)
VALUES (sqlc.arg(chat_id), sqlc.arg(default_provider), sqlc.arg(default_model), sqlc.arg(show_usage), sqlc.arg(updated_at))
ON CONFLICT(chat_id) DO UPDATE SET
    default_provider=excluded.default_provider,
    default_model=excluded.default_model,
    show_usage=excluded.show_usage,
    updated_at=excluded.updated_at
RETURNING *;

-- name: GetAIChatSettings :one
SELECT * FROM ai_chat_settings WHERE chat_id = ?;

-- name: SetAIChatModelSetting :one
INSERT INTO ai_chat_settings(chat_id, default_provider, default_model, show_usage, updated_at)
VALUES (sqlc.arg(chat_id), sqlc.arg(default_provider), sqlc.arg(default_model), 0, sqlc.arg(updated_at))
ON CONFLICT(chat_id) DO UPDATE SET
    default_provider=excluded.default_provider,
    default_model=excluded.default_model,
    updated_at=excluded.updated_at
RETURNING *;

-- name: ToggleAIChatSettingsUsage :one
INSERT INTO ai_chat_settings(chat_id, default_provider, default_model, show_usage, updated_at)
VALUES (sqlc.arg(chat_id), sqlc.arg(default_provider), sqlc.arg(default_model), 1, sqlc.arg(updated_at))
ON CONFLICT(chat_id) DO UPDATE SET
    show_usage=NOT ai_chat_settings.show_usage,
    updated_at=excluded.updated_at
RETURNING *;

-- name: CreateAISession :one
INSERT INTO ai_sessions(chat_id, topic_id, chat_name, chat_type, provider, model, created_at, updated_at)
VALUES (sqlc.arg(chat_id), sqlc.narg(topic_id), sqlc.arg(chat_name), sqlc.arg(chat_type),
        sqlc.arg(provider), sqlc.arg(model), sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING *;

-- name: CreateMigratedAISession :exec
INSERT INTO ai_sessions(id, chat_id, topic_id, chat_name, chat_type, status, provider, model,
                        created_at, updated_at, total_input_tokens, total_output_tokens,
                        total_cached_input_tokens, history_rebuild_lossy)
VALUES (sqlc.arg(id), sqlc.arg(chat_id), NULL, sqlc.arg(chat_name), sqlc.arg(chat_type),
        sqlc.arg(status), sqlc.arg(provider), sqlc.arg(model), sqlc.arg(created_at),
        sqlc.arg(updated_at), sqlc.arg(total_input_tokens), sqlc.arg(total_output_tokens),
        sqlc.arg(total_cached_input_tokens), sqlc.arg(history_rebuild_lossy));

-- name: GetAISession :one
SELECT * FROM ai_sessions WHERE id = ?;

-- name: GetAISessionIDByMessage :one
SELECT sm.session_id
FROM ai_session_messages AS sm
JOIN ai_sessions AS s ON s.id = sm.session_id
WHERE sm.chat_id = sqlc.arg(chat_id) AND sm.msg_id = sqlc.arg(msg_id)
  AND sm.context_only=0
ORDER BY s.updated_at DESC, sm.session_id DESC
LIMIT 1;

-- name: SetAISessionModel :exec
UPDATE ai_sessions
SET provider=sqlc.arg(provider), model=sqlc.arg(model), history_rebuild_lossy=1,
    updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(session_id);

-- name: SetAISessionStatus :exec
UPDATE ai_sessions
SET status=sqlc.arg(status), updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(session_id);

-- name: TouchAISession :exec
UPDATE ai_sessions SET updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(session_id);

-- name: ClearAISessionHistoryRebuildLossy :exec
UPDATE ai_sessions
SET history_rebuild_lossy=0, updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(session_id);

-- name: IncrementAISessionUsage :exec
UPDATE ai_sessions
SET total_input_tokens=total_input_tokens + sqlc.arg(input_tokens),
    total_output_tokens=total_output_tokens + sqlc.arg(output_tokens),
    total_cached_input_tokens=total_cached_input_tokens + sqlc.arg(cached_input_tokens),
    updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(session_id);

-- name: InsertAIMessage :exec
INSERT INTO ai_messages(chat_id, msg_id, sent_at, user_id, username, atable_username,
                        msg_type, text, reply_to_msg_id)
VALUES (sqlc.arg(chat_id), sqlc.arg(msg_id), sqlc.arg(sent_at), sqlc.arg(user_id),
        sqlc.arg(username), sqlc.narg(atable_username), sqlc.arg(msg_type), sqlc.narg(text),
        sqlc.narg(reply_to_msg_id))
ON CONFLICT(chat_id, msg_id) DO NOTHING;

-- name: GetAIMessage :one
SELECT * FROM ai_messages WHERE chat_id=? AND msg_id=?;

-- name: InsertMediaObject :exec
INSERT INTO media_objects(sha256, relative_path, byte_size, mime_type, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(sha256) DO NOTHING;

-- name: GetMediaObject :one
SELECT * FROM media_objects WHERE sha256=?;

-- name: ListReferencedMediaHashes :many
SELECT DISTINCT media_sha256 FROM ai_message_media ORDER BY media_sha256;

-- name: AddAIMessageMedia :exec
INSERT INTO ai_message_media(chat_id, msg_id, ordinal, media_sha256, media_kind)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, msg_id, ordinal) DO NOTHING;

-- name: ListAIMessageMedia :many
SELECT m.*
FROM media_objects m
JOIN ai_message_media mm ON mm.media_sha256=m.sha256
WHERE mm.chat_id=? AND mm.msg_id=?
ORDER BY mm.ordinal;

-- name: AddAISessionMessage :exec
INSERT INTO ai_session_messages(session_id, position, chat_id, msg_id, role, quote_part, context_only)
VALUES (sqlc.arg(session_id), sqlc.arg(position), sqlc.arg(chat_id), sqlc.arg(msg_id),
        sqlc.arg(role), sqlc.narg(quote_part), sqlc.arg(context_only))
ON CONFLICT(session_id, chat_id, msg_id) DO NOTHING;

-- name: GetNextAISessionMessagePosition :one
SELECT CAST(COALESCE(MAX(position) + 1, 0) AS INTEGER)
FROM ai_session_messages
WHERE session_id = ?;

-- name: MarkAIMessageAsUserInput :exec
UPDATE ai_session_messages
SET role = 'user'
WHERE chat_id = sqlc.arg(chat_id) AND msg_id = sqlc.arg(msg_id)
  AND NOT EXISTS (
      SELECT 1 FROM ai_runs
      WHERE response_chat_id = sqlc.arg(chat_id) AND response_msg_id = sqlc.arg(msg_id)
  );

-- name: ListAISessionMessages :many
SELECT sm.*, m.sent_at, m.user_id, m.username, m.atable_username, m.msg_type, m.text, m.reply_to_msg_id
FROM ai_session_messages sm
JOIN ai_messages m ON m.chat_id=sm.chat_id AND m.msg_id=sm.msg_id
WHERE sm.session_id=?
  AND sm.context_only=0
ORDER BY sm.position;

-- name: CreateAIRun :one
INSERT INTO ai_runs(session_id, request_chat_id, request_msg_id, status, provider, model, requested_at)
VALUES (sqlc.arg(session_id), sqlc.arg(request_chat_id), sqlc.arg(request_msg_id),
        'pending', sqlc.arg(provider), sqlc.arg(model), sqlc.arg(requested_at))
ON CONFLICT(session_id, request_chat_id, request_msg_id) DO UPDATE SET id=id
RETURNING *;

-- name: GetAIRunByRequest :one
SELECT * FROM ai_runs
WHERE session_id=? AND request_chat_id=? AND request_msg_id=?;

-- name: GetAIRun :one
SELECT * FROM ai_runs WHERE id=?;

-- name: GetAIRunByResponse :one
SELECT * FROM ai_runs WHERE response_chat_id=? AND response_msg_id=?;

-- name: ListAISessionAssistantRuns :many
SELECT * FROM ai_runs
WHERE session_id=? AND status='delivered'
  AND response_msg_id IS NOT NULL
  AND assistant_payload IS NOT NULL
  AND assistant_payload_format IS NOT NULL
ORDER BY requested_at, id;

-- name: MarkAIRunGenerated :execrows
UPDATE ai_runs
SET status='generated', completed_at=sqlc.arg(completed_at),
    input_tokens=sqlc.narg(input_tokens), output_tokens=sqlc.narg(output_tokens),
    cached_input_tokens=sqlc.narg(cached_input_tokens),
    input_message_count=sqlc.narg(input_message_count),
    input_first_msg_id=sqlc.narg(input_first_msg_id), input_last_msg_id=sqlc.narg(input_last_msg_id),
    response_text=sqlc.narg(response_text), raw_payload=sqlc.narg(raw_payload),
    thought_signature=sqlc.narg(thought_signature),
    assistant_payload=sqlc.narg(assistant_payload), assistant_payload_format=sqlc.narg(assistant_payload_format),
    cache_expire_at=sqlc.narg(cache_expire_at), candidate_state_json=sqlc.narg(candidate_state_json),
    error_code=NULL, error_message=NULL
WHERE id=sqlc.arg(run_id) AND status='pending';

-- name: MarkAIRunDelivered :execrows
UPDATE ai_runs
SET status='delivered', response_chat_id=sqlc.arg(response_chat_id),
    response_msg_id=sqlc.arg(response_msg_id), error_code=NULL, error_message=NULL
WHERE id=sqlc.arg(run_id) AND status IN ('generated', 'delivery_failed');

-- name: MarkAIRunFailed :execrows
UPDATE ai_runs
SET status=sqlc.arg(status), completed_at=sqlc.arg(completed_at),
    error_code=sqlc.narg(error_code), error_message=sqlc.narg(error_message)
WHERE id=sqlc.arg(run_id) AND status IN ('pending', 'generated', 'delivery_failed');

-- name: UpsertAISessionProviderState :exec
INSERT INTO ai_session_provider_state(session_id, provider, state_version, state_json, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    provider=excluded.provider, state_version=excluded.state_version,
    state_json=excluded.state_json, updated_at=excluded.updated_at;

-- name: GetAISessionProviderState :one
SELECT * FROM ai_session_provider_state WHERE session_id=?;

-- name: DeleteAISessionProviderState :exec
DELETE FROM ai_session_provider_state WHERE session_id=?;

-- name: UpsertAISystemPrompt :exec
INSERT INTO ai_system_prompts(chat_id, topic_id, prompt, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id) DO UPDATE SET prompt=excluded.prompt, updated_at=excluded.updated_at;

-- name: GetAISystemPrompt :one
SELECT prompt FROM ai_system_prompts WHERE chat_id=? AND topic_id=?;

-- name: DeleteAISystemPrompt :exec
DELETE FROM ai_system_prompts WHERE chat_id=? AND topic_id=?;

-- name: RecordSchemaMigration :exec
INSERT INTO schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, ?);

-- name: GetAIMigrationStats :one
SELECT
  CAST((SELECT COUNT(*) FROM ai_sessions) AS INTEGER) AS sessions,
  CAST((SELECT COUNT(*) FROM ai_messages) AS INTEGER) AS messages,
  CAST((SELECT COUNT(*) FROM ai_session_messages) AS INTEGER) AS session_messages,
  CAST((SELECT COUNT(*) FROM ai_runs) AS INTEGER) AS runs,
  CAST((SELECT COUNT(*) FROM ai_system_prompts) AS INTEGER) AS prompts,
  CAST((SELECT COUNT(*) FROM ai_chat_settings) AS INTEGER) AS chat_settings,
  CAST((SELECT COUNT(*) FROM media_objects) AS INTEGER) AS media_objects,
  CAST((SELECT COUNT(*) FROM ai_message_media) AS INTEGER) AS media_references,
  CAST((SELECT COUNT(*) FROM ai_runs WHERE length(assistant_payload) > 0) AS INTEGER) AS assistant_payloads,
  CAST(COALESCE((SELECT SUM(total_input_tokens) FROM ai_sessions), 0) AS INTEGER) AS input_tokens,
  CAST(COALESCE((SELECT SUM(total_output_tokens) FROM ai_sessions), 0) AS INTEGER) AS output_tokens,
  CAST(COALESCE((SELECT SUM(total_cached_input_tokens) FROM ai_sessions), 0) AS INTEGER) AS cached_input_tokens;
