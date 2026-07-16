-- encoding: utf-8

-- name: GetBiliInlineData :one
SELECT text, chat_id, msg_id
FROM bili_inline_results
WHERE uid = ?
  AND created_at >= ?;


-- name: CreateBiliInlineData :one
INSERT INTO bili_inline_results(created_at)
VALUES (?)
RETURNING uid;

-- name: UpdateBiliInlineMsgId :exec
UPDATE bili_inline_results
SET text    = ?,
    chat_id = ?,
    msg_id  = ?
WHERE uid = ?;

-- name: DeleteExpiredBiliInlineData :execrows
DELETE FROM bili_inline_results
WHERE created_at < ?;
