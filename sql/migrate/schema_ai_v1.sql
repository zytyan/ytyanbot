CREATE TABLE gemini_sessions (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    chat_name TEXT NOT NULL,
    chat_type TEXT NOT NULL,
    frozen INTEGER NOT NULL,
    total_input_tokens INTEGER NOT NULL,
    total_output_tokens INTEGER NOT NULL
);

CREATE TABLE gemini_contents (
    session_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    msg_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    sent_time INTEGER NOT NULL,
    username TEXT NOT NULL,
    msg_type TEXT NOT NULL,
    reply_to_msg_id INTEGER,
    text TEXT,
    blob BLOB,
    mime_type TEXT,
    quote_part TEXT,
    thought_signature TEXT,
    atable_username TEXT,
    user_id INTEGER NOT NULL,
    PRIMARY KEY(session_id, msg_id)
);

CREATE TABLE gemini_system_prompt (
    chat_id INTEGER NOT NULL,
    thread_id INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    PRIMARY KEY(chat_id, thread_id)
);

CREATE TABLE ai_chat_models (
    chat_id INTEGER PRIMARY KEY,
    model TEXT NOT NULL,
    show_usage INTEGER NOT NULL
);

CREATE TABLE ai_session_meta (
    session_id INTEGER PRIMARY KEY,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    cached_input_tokens INTEGER NOT NULL,
    gemini_interaction_id TEXT,
    window_start_msg_id INTEGER,
    gemini_cache_name TEXT,
    gemini_cache_expire_time INTEGER,
    gemini_cache_start_msg_id INTEGER,
    gemini_cache_end_msg_id INTEGER,
    gemini_cache_token_count INTEGER NOT NULL,
    gemini_cache_fingerprint TEXT,
    history_rebuild_lossy INTEGER NOT NULL
);

CREATE TABLE ai_message_meta (
    session_id INTEGER NOT NULL,
    msg_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    cached_input_tokens INTEGER NOT NULL,
    input_message_count INTEGER NOT NULL,
    input_first_msg_id INTEGER NOT NULL,
    input_last_msg_id INTEGER NOT NULL,
    assistant_payload BLOB,
    assistant_payload_format TEXT,
    PRIMARY KEY(session_id, msg_id)
);
