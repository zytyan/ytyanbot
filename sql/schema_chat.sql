CREATE TABLE IF NOT EXISTS chat_cfg
(
    id               INTEGER PRIMARY KEY NOT NULL,
    auto_cvt_bili    INT_BOOL            NOT NULL CHECK ( auto_cvt_bili in (0, 1)),
    auto_calculate   INT_BOOL            NOT NULL CHECK ( auto_calculate in (0, 1)),
    auto_exchange    INT_BOOL            NOT NULL CHECK ( auto_exchange in (0, 1)),
    auto_check_adult INT_BOOL            NOT NULL CHECK ( auto_check_adult in (0, 1)),
    enable_coc       INT_BOOL            NOT NULL CHECK ( enable_coc in (0, 1)),
    resp_nsfw_msg    INT_BOOL            NOT NULL CHECK ( resp_nsfw_msg in (0, 1)),
    timezone         INTEGER             NOT NULL CHECK ( timezone < 86400 AND timezone > -86400)
);

CREATE TABLE chat_stat_daily
(
    chat_id              INTEGER              NOT NULL,
    stat_date            INTEGER              NOT NULL, -- 从Unix纪元开始的日期数量

    message_count        INTEGER              NOT NULL DEFAULT 0,
    photo_count          INTEGER              NOT NULL DEFAULT 0,
    video_count          INTEGER              NOT NULL DEFAULT 0,
    sticker_count        INTEGER              NOT NULL DEFAULT 0,
    forward_count        INTEGER              NOT NULL DEFAULT 0,

    mars_count           INTEGER              NOT NULL DEFAULT 0,
    max_mars_count       INTEGER              NOT NULL DEFAULT 0,

    racy_count           INTEGER              NOT NULL DEFAULT 0,
    adult_count          INTEGER              NOT NULL DEFAULT 0,

    download_video_count INTEGER              NOT NULL DEFAULT 0,
    download_audio_count INTEGER              NOT NULL DEFAULT 0,

    dio_add_user_count   INTEGER              NOT NULL DEFAULT 0,
    dio_ban_user_count   INTEGER              NOT NULL DEFAULT 0,

    PRIMARY KEY (chat_id, stat_date)
) WITHOUT ROWID;

CREATE TABLE chat_stat_user_daily
(
    chat_id        INTEGER NOT NULL,
    stat_date      INTEGER NOT NULL,
    user_id        INTEGER NOT NULL,
    message_count  INTEGER NOT NULL CHECK (message_count >= 0),
    message_length INTEGER NOT NULL CHECK (message_length >= 0),
    PRIMARY KEY (chat_id, stat_date, user_id),
    FOREIGN KEY (chat_id, stat_date) REFERENCES chat_stat_daily(chat_id, stat_date) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;

CREATE TABLE chat_stat_bucket_daily
(
    chat_id       INTEGER NOT NULL,
    stat_date     INTEGER NOT NULL,
    bucket        INTEGER NOT NULL CHECK (bucket BETWEEN 0 AND 143),
    message_count INTEGER NOT NULL CHECK (message_count >= 0),
    first_msg_id  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, stat_date, bucket),
    FOREIGN KEY (chat_id, stat_date) REFERENCES chat_stat_daily(chat_id, stat_date) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;
