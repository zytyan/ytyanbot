-- encoding: utf-8

-- name: CreateNewGeminiSession :one
INSERT INTO gemini_sessions (chat_id, chat_name, chat_type)
VALUES (?, ?, ?)
RETURNING *;

-- name: AddGeminiMessage :exec
INSERT INTO gemini_contents (session_id,
                             chat_id,
                             msg_id,
                             role,
                             sent_time,
                             username,
                             msg_type,
                             reply_to_msg_id,
                             text,
                             blob,
                             mime_type,
                             quote_part,
                             thought_signature,
                             atable_username,
                             user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: getAllMsgInSessionReversed :many
SELECT *
FROM gemini_contents
WHERE session_id = ?
ORDER BY msg_id DESC
LIMIT ?;

-- name: GetSessionIdByMessage :one
SELECT gemini_contents.session_id
FROM gemini_contents
WHERE chat_id = ?
  AND msg_id = ?;

-- name: GetSessionById :one
SELECT *
FROM gemini_sessions
WHERE id = ?;

-- name: CreateOrUpdateGeminiSystemPrompt :exec
INSERT INTO gemini_system_prompt (chat_id, thread_id, prompt)
VALUES (?, ?, ?)
ON CONFLICT DO UPDATE SET prompt=excluded.prompt;

-- name: GetGeminiSystemPrompt :one
SELECT prompt
FROM gemini_system_prompt
WHERE chat_id = ?
  AND thread_id = ?;

-- name: ResetGeminiSystemPrompt :exec
DELETE
FROM gemini_system_prompt
WHERE chat_id = ?
  AND thread_id = ?;

-- name: IncrementSessionTokenCounters :exec
UPDATE gemini_sessions
SET total_input_tokens = total_input_tokens + ?,
    total_output_tokens=total_output_tokens + ?
WHERE id = ?;
