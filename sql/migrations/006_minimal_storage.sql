-- Populated pre-v6 databases must be converted offline with db-import.
-- The offline source snapshot and media archive retain omitted historical data.
DROP TABLE ai_run_context;
DROP TABLE import_archives;
ALTER TABLE ai_runs DROP COLUMN raw_payload;
ALTER TABLE ai_runs DROP COLUMN thought_signature;
ALTER TABLE ai_runs DROP COLUMN cache_expire_at;
ALTER TABLE ai_sessions DROP COLUMN total_input_tokens;
ALTER TABLE ai_sessions DROP COLUMN total_output_tokens;
ALTER TABLE ai_sessions DROP COLUMN total_cached_input_tokens;
DROP TABLE ai_message_media;
DROP TABLE media_objects;
CREATE TABLE media_objects (
 file_unique_id TEXT PRIMARY KEY CHECK(length(file_unique_id)>0),
 file_id TEXT NOT NULL CHECK(length(file_id)>0),
 byte_size INTEGER NOT NULL CHECK(byte_size>=0),
 mime_type TEXT NOT NULL CHECK(length(mime_type)>0)
) WITHOUT ROWID, STRICT;
CREATE TABLE ai_message_media (
 chat_id INTEGER NOT NULL, msg_id INTEGER NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 file_unique_id TEXT REFERENCES media_objects(file_unique_id),
 media_kind TEXT NOT NULL,
 unavailable INTEGER NOT NULL DEFAULT 0 CHECK(unavailable IN (0,1)),
 CHECK((file_unique_id IS NULL) = (unavailable=1)),
 PRIMARY KEY(chat_id,msg_id,ordinal),
 FOREIGN KEY(chat_id,msg_id) REFERENCES ai_messages(chat_id,msg_id)
) WITHOUT ROWID, STRICT;
CREATE INDEX idx_ai_message_media_file ON ai_message_media(file_unique_id);
-- Delivered display text already lives on the linked Telegram reply message.
UPDATE ai_runs SET response_text=NULL WHERE status='delivered' AND response_text=(
 SELECT text FROM ai_messages WHERE chat_id=ai_runs.response_chat_id AND msg_id=ai_runs.response_msg_id
);
