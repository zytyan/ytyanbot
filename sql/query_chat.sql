-- encoding: utf-8

-- name: getChatCfgById :one
SELECT *
FROM chat_cfg
WHERE id = ?;

-- name: CreateChatCfg :exec
INSERT INTO chat_cfg (id, auto_cvt_bili, auto_calculate, auto_exchange, auto_check_adult,
                      enable_coc, resp_nsfw_msg, timezone)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: updateChatCfg :exec
UPDATE chat_cfg
SET auto_cvt_bili=?,
    auto_calculate=?,
    auto_exchange=?,
    auto_check_adult=?,
    enable_coc=?,
    resp_nsfw_msg=?
WHERE id = ?;

-- name: createChatStatDaily :one
INSERT INTO chat_stat_daily (chat_id, stat_date)
VALUES (?, ?)
RETURNING *;

-- name: UpdateChatStatDaily :exec
UPDATE chat_stat_daily
SET message_count        = ?,
    photo_count          = ?,
    video_count          = ?,
    sticker_count        = ?,
    forward_count        = ?,
    mars_count           = ?,
    max_mars_count       = ?,
    racy_count           = ?,
    adult_count          = ?,
    download_video_count = ?,
    download_audio_count = ?,
    dio_add_user_count   = ?,
    dio_ban_user_count   = ?
WHERE chat_id = ?
  AND stat_date = ?;

-- name: getChatStat :one
SELECT *
FROM chat_stat_daily
WHERE chat_stat_daily.chat_id = ?
  AND chat_stat_daily.stat_date = ?;

-- name: ListChatStatUsers :many
SELECT user_id, message_count, message_length
FROM chat_stat_user_daily
WHERE chat_id=? AND stat_date=?
ORDER BY user_id;

-- name: UpsertChatStatUser :exec
INSERT INTO chat_stat_user_daily(chat_id, stat_date, user_id, message_count, message_length)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, stat_date, user_id) DO UPDATE SET
    message_count=excluded.message_count,
    message_length=excluded.message_length;

-- name: ListChatStatBuckets :many
SELECT bucket, message_count, first_msg_id
FROM chat_stat_bucket_daily
WHERE chat_id=? AND stat_date=?
ORDER BY bucket;

-- name: UpsertChatStatBucket :exec
INSERT INTO chat_stat_bucket_daily(chat_id, stat_date, bucket, message_count, first_msg_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, stat_date, bucket) DO UPDATE SET
    message_count=excluded.message_count,
    first_msg_id=excluded.first_msg_id;
