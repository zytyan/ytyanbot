-- encoding: utf-8

-- name: getUserById :one
SELECT *
FROM users
WHERE user_id = ?;

-- name: createNewUser :one
INSERT INTO users (updated_at, user_id, first_name, last_name, username)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: updateUserBase :one
UPDATE users
SET updated_at=?2,
    first_name=?3,
    last_name =?4,
    username  = ?5
WHERE user_id = ?1
RETURNING *;

-- name: SetPrprCache :exec
INSERT INTO prpr_caches (profile_photo_uid, prpr_file_id)
VALUES (?, ?)
ON CONFLICT(profile_photo_uid) DO UPDATE SET prpr_file_id=excluded.prpr_file_id;

-- name: GetPrprCache :one
SELECT prpr_file_id
FROM prpr_caches
WHERE profile_photo_uid = ?;
