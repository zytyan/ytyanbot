-- encoding: utf-8

CREATE TABLE IF NOT EXISTS users
(
    user_id    INTEGER      NOT NULL PRIMARY KEY,
    updated_at INT_UNIX_SEC NOT NULL,
    first_name TEXT         NOT NULL,
    last_name  TEXT,
    username   TEXT
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS prpr_caches
(
    profile_photo_uid TEXT NOT NULL PRIMARY KEY,
    prpr_file_id      TEXT NOT NULL
) WITHOUT ROWID;
