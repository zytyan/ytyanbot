-- name: GetReactions
SELECT reactions_enabled FROM ai_user_settings WHERE user_id=?;

-- name: UsageForResponse
SELECT session_id,provider,model,input_tokens,output_tokens,cached_input_tokens,
input_message_count,input_first_msg_id,input_last_msg_id,json_extract(candidate_state_json,'$.geminiCacheExpireTime') AS cache_expire_at
FROM ai_runs WHERE response_chat_id=? AND response_msg_id=? AND status='delivered';

-- name: SetReactions
INSERT INTO ai_user_settings(user_id,reactions_enabled,updated_at) VALUES(?,?,?)
ON CONFLICT(user_id) DO UPDATE SET reactions_enabled=excluded.reactions_enabled,updated_at=excluded.updated_at;

-- name: IsAiResponse
SELECT r.id FROM ai_runs r JOIN ai_sessions s ON s.id=r.session_id
WHERE r.status='delivered' AND r.response_chat_id=? AND r.response_msg_id=? AND COALESCE(s.topic_id,0)=?;

-- name: SaveFact
INSERT INTO traditional_facts(feature,chat_id,topic_id,request_msg_id,reply_msg_id,status,context_text,created_at,delivery_status)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: ReplyFacts
SELECT feature,request_msg_id,reply_msg_id,status,context_text,created_at FROM traditional_facts
WHERE chat_id=? AND topic_id=? AND reply_msg_id=? AND delivery_status='delivered' ORDER BY id;

-- name: RequestFacts
SELECT feature,request_msg_id,reply_msg_id,status,context_text,created_at FROM traditional_facts
WHERE chat_id=? AND topic_id=? AND request_msg_id=? ORDER BY id;

-- name: RunForRequest
SELECT * FROM ai_runs WHERE request_chat_id=? AND request_msg_id=? ORDER BY id DESC LIMIT 1;

-- name: GetPrprCache
SELECT prpr_file_id FROM prpr_caches WHERE profile_photo_uid=?;

-- name: SetPrprCache
INSERT INTO prpr_caches(profile_photo_uid,prpr_file_id) VALUES(?,?)
ON CONFLICT(profile_photo_uid) DO UPDATE SET prpr_file_id=excluded.prpr_file_id;

-- name: EnsureChatConfig
INSERT INTO chat_cfg(id,auto_cvt_bili,auto_calculate,auto_exchange,auto_check_adult,enable_coc,resp_nsfw_msg,timezone)
VALUES(?,0,0,0,0,0,0,?) ON CONFLICT(id) DO NOTHING;

-- name: SetChatConfig
UPDATE chat_cfg SET auto_cvt_bili=?,auto_calculate=?,auto_exchange=?,auto_check_adult=?,resp_nsfw_msg=? WHERE id=?;

-- name: BackupMedia
SELECT file_unique_id,file_id,byte_size,mime_type FROM media_objects ORDER BY file_unique_id;

-- name: IndexAlbum
INSERT INTO album_index(chat_id,topic_id,group_id,expires_at,photo_only) VALUES(?,?,?,?,?)
ON CONFLICT(chat_id,topic_id,group_id) DO UPDATE SET expires_at=MAX(expires_at,excluded.expires_at),photo_only=MIN(photo_only,excluded.photo_only);

-- name: IndexAlbumFile
INSERT INTO album_files(chat_id,topic_id,group_id,msg_id,file_id,file_unique_id,byte_size) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(chat_id,topic_id,group_id,msg_id) DO UPDATE SET file_id=excluded.file_id,file_unique_id=excluded.file_unique_id,byte_size=excluded.byte_size;

-- name: GetAlbum
SELECT topic_id,expires_at,photo_only FROM album_index WHERE chat_id=? AND topic_id IN (?, -1) AND group_id=? ORDER BY topic_id DESC LIMIT 1;

-- name: AlbumFiles
SELECT msg_id,file_id,file_unique_id,byte_size FROM album_files WHERE chat_id=? AND topic_id=? AND group_id=? ORDER BY msg_id;

-- name: ExpireAlbums
DELETE FROM album_index WHERE expires_at<=?;

-- name: AlbumForMessage
SELECT group_id FROM album_files WHERE chat_id=? AND topic_id IN (?, -1) AND msg_id=? ORDER BY topic_id DESC LIMIT 1;

-- name: ImportedAlbumGroups
SELECT chat_id,topic_id,group_id,expires_at,photo_only FROM album_index WHERE topic_id=-1 ORDER BY chat_id,group_id;

-- name: ImportedAlbumFiles
SELECT chat_id,topic_id,group_id,msg_id,file_id,file_unique_id,byte_size FROM album_files WHERE topic_id=-1 ORDER BY chat_id,msg_id;


-- name: SessionHasMessage
SELECT 1 AS found FROM ai_session_messages WHERE session_id=? AND chat_id=? AND msg_id=?;
-- name: SetSessionSuspension
INSERT INTO ai_session_suspensions(session_id,until_at) VALUES(?,?)
ON CONFLICT(session_id) DO UPDATE SET until_at=excluded.until_at;

-- name: ResumeSession
DELETE FROM ai_session_suspensions WHERE session_id=?;

-- name: IsSessionSuspended
SELECT 1 AS found FROM ai_session_suspensions WHERE session_id=? AND (until_at IS NULL OR until_at>?);

-- name: GetSessionRequestOrigin
SELECT suppressed FROM ai_session_request_origins WHERE session_id=? AND chat_id=? AND msg_id=?;

-- name: LastSessionRequestOrigin
SELECT bot_streak FROM ai_session_request_origins WHERE session_id=? ORDER BY id DESC LIMIT 1;

-- name: SaveSessionRequestOrigin
INSERT INTO ai_session_request_origins(session_id,chat_id,msg_id,bot_streak,suppressed) VALUES(?,?,?,?,?);

-- name: IsLoopSuppressedRequest
SELECT 1 AS found FROM ai_session_request_origins WHERE chat_id=? AND msg_id=? AND suppressed=1 LIMIT 1;

-- name: SaveMedia
INSERT INTO media_objects(file_unique_id,file_id,byte_size,mime_type) VALUES(?,?,?,?)
ON CONFLICT(file_unique_id) DO UPDATE SET file_id=excluded.file_id;

-- name: SaveMessageMedia
INSERT INTO ai_message_media(chat_id,msg_id,ordinal,file_unique_id,media_kind) VALUES(?,?,?,?,?)
ON CONFLICT(chat_id,msg_id,ordinal) DO NOTHING;

-- name: MessageMedia
SELECT o.file_unique_id,o.file_id,o.byte_size,o.mime_type,mm.media_kind,mm.unavailable
FROM ai_message_media mm LEFT JOIN media_objects o USING(file_unique_id)
WHERE mm.chat_id=? AND mm.msg_id=? ORDER BY mm.ordinal;

-- name: UpdateMediaFileId
UPDATE media_objects SET file_id=? WHERE file_unique_id=?;

-- name: SessionUsage
SELECT COALESCE(SUM(input_tokens),0) AS total_input_tokens,
COALESCE(SUM(output_tokens),0) AS total_output_tokens,
COALESCE(SUM(cached_input_tokens),0) AS total_cached_input_tokens FROM ai_runs WHERE session_id=?;

-- name: UnavailableMediaCount
SELECT COUNT(*) AS count FROM ai_message_media WHERE unavailable=1;
