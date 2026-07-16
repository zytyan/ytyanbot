CREATE TABLE schema_migrations
(
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;

CREATE TABLE ai_chat_settings
(
    chat_id          INTEGER PRIMARY KEY,
    default_provider TEXT    NOT NULL CHECK (length(default_provider) > 0),
    default_model    TEXT    NOT NULL CHECK (length(default_model) > 0),
    show_usage       INTEGER NOT NULL DEFAULT 0 CHECK (show_usage IN (0, 1)),
    updated_at       INTEGER NOT NULL
) WITHOUT ROWID, STRICT;

CREATE TABLE ai_sessions
(
    id                        INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id                   INTEGER NOT NULL,
    topic_id                  INTEGER CHECK (topic_id IS NULL OR topic_id >= 0),
    chat_name                 TEXT    NOT NULL,
    chat_type                 TEXT    NOT NULL,
    status                    TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed', 'frozen')),
    provider                  TEXT    NOT NULL CHECK (length(provider) > 0),
    model                     TEXT    NOT NULL CHECK (length(model) > 0),
    created_at                INTEGER NOT NULL,
    updated_at                INTEGER NOT NULL,
    total_input_tokens        INTEGER NOT NULL DEFAULT 0 CHECK (total_input_tokens >= 0),
    total_output_tokens       INTEGER NOT NULL DEFAULT 0 CHECK (total_output_tokens >= 0),
    total_cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_cached_input_tokens >= 0),
    history_rebuild_lossy     INTEGER NOT NULL DEFAULT 0 CHECK (history_rebuild_lossy IN (0, 1))
) STRICT;

CREATE INDEX idx_ai_sessions_chat_topic_updated
    ON ai_sessions (chat_id, topic_id, updated_at DESC);

CREATE TABLE ai_messages
(
    chat_id           INTEGER NOT NULL,
    msg_id            INTEGER NOT NULL,
    sent_at           INTEGER NOT NULL,
    user_id           INTEGER NOT NULL,
    username          TEXT    NOT NULL,
    atable_username   TEXT,
    msg_type          TEXT    NOT NULL CHECK (length(msg_type) > 0),
    text              TEXT,
    reply_to_msg_id   INTEGER,
    PRIMARY KEY (chat_id, msg_id)
) WITHOUT ROWID, STRICT;

CREATE TABLE media_objects
(
    sha256        TEXT PRIMARY KEY CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
    relative_path TEXT    NOT NULL UNIQUE,
    byte_size     INTEGER NOT NULL CHECK (byte_size >= 0),
    mime_type     TEXT    NOT NULL CHECK (length(mime_type) > 0),
    created_at    INTEGER NOT NULL
) WITHOUT ROWID, STRICT;

CREATE TABLE ai_message_media
(
    chat_id      INTEGER NOT NULL,
    msg_id       INTEGER NOT NULL,
    ordinal      INTEGER NOT NULL CHECK (ordinal >= 0),
    media_sha256 TEXT    NOT NULL,
    media_kind   TEXT    NOT NULL CHECK (length(media_kind) > 0),
    PRIMARY KEY (chat_id, msg_id, ordinal),
    FOREIGN KEY (chat_id, msg_id) REFERENCES ai_messages (chat_id, msg_id) ON DELETE CASCADE,
    FOREIGN KEY (media_sha256) REFERENCES media_objects (sha256) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX idx_ai_message_media_hash ON ai_message_media (media_sha256);

CREATE TABLE ai_session_messages
(
    session_id  INTEGER NOT NULL,
    position    INTEGER NOT NULL CHECK (position >= 0),
    chat_id     INTEGER NOT NULL,
    msg_id      INTEGER NOT NULL,
    role        TEXT    NOT NULL CHECK (role IN ('user', 'model', 'system')),
    quote_part  TEXT,
    context_only INTEGER NOT NULL DEFAULT 0 CHECK (context_only IN (0, 1)),
    PRIMARY KEY (session_id, position),
    UNIQUE (session_id, chat_id, msg_id),
    FOREIGN KEY (session_id) REFERENCES ai_sessions (id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id, msg_id) REFERENCES ai_messages (chat_id, msg_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX idx_ai_session_messages_chat_msg_context
    ON ai_session_messages (chat_id, msg_id, context_only);

CREATE TABLE ai_runs
(
    id                         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id                 INTEGER NOT NULL,
    request_chat_id            INTEGER NOT NULL,
    request_msg_id             INTEGER NOT NULL,
    status                     TEXT    NOT NULL CHECK (status IN ('pending', 'generated', 'delivered', 'delivery_failed', 'model_failed', 'persistence_failed')),
    provider                   TEXT    NOT NULL CHECK (length(provider) > 0),
    model                      TEXT    NOT NULL CHECK (length(model) > 0),
    requested_at               INTEGER NOT NULL,
    completed_at               INTEGER,
    input_tokens               INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    output_tokens              INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    cached_input_tokens        INTEGER CHECK (cached_input_tokens IS NULL OR cached_input_tokens >= 0),
    input_message_count        INTEGER CHECK (input_message_count IS NULL OR input_message_count >= 0),
    input_first_msg_id         INTEGER,
    input_last_msg_id          INTEGER,
    response_text              TEXT,
    raw_payload                BLOB,
    thought_signature          TEXT,
    assistant_payload          BLOB,
    assistant_payload_format   TEXT,
    cache_expire_at            INTEGER,
    candidate_state_json       TEXT CHECK (candidate_state_json IS NULL OR json_valid(candidate_state_json)),
    response_chat_id           INTEGER,
    response_msg_id            INTEGER,
    error_code                 TEXT,
    error_message              TEXT,
    UNIQUE (session_id, request_chat_id, request_msg_id),
    UNIQUE (response_chat_id, response_msg_id),
    CHECK ((response_chat_id IS NULL) = (response_msg_id IS NULL)),
    CHECK (input_first_msg_id IS NULL OR input_last_msg_id IS NULL OR input_first_msg_id <= input_last_msg_id),
    FOREIGN KEY (session_id) REFERENCES ai_sessions (id) ON DELETE CASCADE,
    FOREIGN KEY (request_chat_id, request_msg_id) REFERENCES ai_messages (chat_id, msg_id) ON DELETE RESTRICT,
    FOREIGN KEY (session_id, response_chat_id, response_msg_id)
        REFERENCES ai_session_messages (session_id, chat_id, msg_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_ai_runs_session_requested ON ai_runs (session_id, requested_at DESC);

CREATE TABLE ai_session_provider_state
(
    session_id    INTEGER PRIMARY KEY,
    provider      TEXT    NOT NULL CHECK (length(provider) > 0),
    state_version INTEGER NOT NULL CHECK (state_version > 0),
    state_json    TEXT    NOT NULL CHECK (json_valid(state_json)),
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES ai_sessions (id) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;

CREATE TABLE ai_system_prompts
(
    chat_id    INTEGER NOT NULL,
    topic_id   INTEGER NOT NULL CHECK (topic_id >= 0),
    prompt     TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (chat_id, topic_id)
) WITHOUT ROWID, STRICT;
