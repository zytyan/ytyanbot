-- name: ListLegacyChatSettings :many
SELECT chat_id, model, show_usage
FROM ai_chat_models
ORDER BY chat_id;

-- name: ListLegacySessions :many
SELECT s.id, s.chat_id, s.chat_name, s.chat_type, s.frozen,
       s.total_input_tokens, s.total_output_tokens,
       CAST(COALESCE(MIN(c.sent_time), 0) AS INTEGER) AS first_message_at,
       CAST(COALESCE(MAX(c.sent_time), 0) AS INTEGER) AS last_message_at,
       m.provider, m.model, m.cached_input_tokens,
       m.gemini_interaction_id, m.window_start_msg_id,
       m.gemini_cache_name, m.gemini_cache_expire_time,
       m.gemini_cache_start_msg_id, m.gemini_cache_end_msg_id,
       m.gemini_cache_token_count, m.gemini_cache_fingerprint,
       m.history_rebuild_lossy
FROM gemini_sessions AS s
LEFT JOIN gemini_contents AS c ON c.session_id=s.id
LEFT JOIN ai_session_meta AS m ON m.session_id=s.id
GROUP BY s.id
ORDER BY s.id;

-- name: ListLegacyMessages :many
SELECT c.session_id, c.chat_id, c.msg_id, c.role, c.sent_time,
       c.username, c.msg_type, c.reply_to_msg_id, c.text, c.blob,
       c.mime_type, c.quote_part, c.thought_signature,
       c.atable_username, c.user_id,
       m.provider AS response_provider, m.model AS response_model,
       m.input_tokens, m.output_tokens, m.cached_input_tokens,
       m.input_message_count, m.input_first_msg_id, m.input_last_msg_id,
       m.assistant_payload, m.assistant_payload_format
FROM gemini_contents AS c
LEFT JOIN ai_message_meta AS m
  ON m.session_id=c.session_id AND m.msg_id=c.msg_id
ORDER BY c.session_id, c.sent_time, c.msg_id;

-- name: ListLegacySystemPrompts :many
SELECT chat_id, thread_id, prompt
FROM gemini_system_prompt
ORDER BY chat_id, thread_id;
