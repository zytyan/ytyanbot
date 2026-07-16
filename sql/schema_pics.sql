-- encoding: utf-8

CREATE TABLE saved_pics
(
    file_uid        TEXT    NOT NULL,
    file_id         TEXT    NOT NULL, -- 插入时，若 file_uid 相同，则更新 file_id
    bot_rate        INTEGER NOT NULL CHECK (bot_rate BETWEEN -1 AND 7), -- -1 时相当于删除
    rand_key        INTEGER NOT NULL,
    -- user_rate 为生成列：有评分时为平均分；否则回退到 bot_rate
    user_rate       INTEGER NOT NULL GENERATED ALWAYS AS (
        CASE
            WHEN rate_user_count > 0
                THEN CAST(ROUND(user_rating_sum * 1.0 / rate_user_count) AS INTEGER)
            ELSE bot_rate
        END
    ) STORED,
    user_rating_sum INTEGER NOT NULL DEFAULT 0,
    rate_user_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (file_uid),
    UNIQUE (user_rate, rand_key),
    UNIQUE (rand_key)                 -- 确保 rand_key 自身唯一
) WITHOUT ROWID, STRICT;


CREATE TABLE IF NOT EXISTS saved_pics_rating
(
    file_uid TEXT    NOT NULL,
    user_id  INTEGER NOT NULL,
    rating   INTEGER NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 7),
    PRIMARY KEY (file_uid, user_id),
    FOREIGN KEY (file_uid) REFERENCES saved_pics (file_uid) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;


CREATE TRIGGER IF NOT EXISTS saved_pics_rating_insert_trigger
    AFTER INSERT
    ON saved_pics_rating
BEGIN
    UPDATE saved_pics
    SET user_rating_sum = user_rating_sum + new.rating,
        rate_user_count = rate_user_count + 1
    WHERE file_uid = new.file_uid;
END;

CREATE TRIGGER IF NOT EXISTS saved_pics_rating_update_trigger
    AFTER UPDATE
    ON saved_pics_rating
BEGIN
    UPDATE saved_pics
    SET user_rating_sum = user_rating_sum - old.rating + new.rating
    WHERE file_uid = old.file_uid;
END;

CREATE TRIGGER IF NOT EXISTS saved_pics_rating_delete_trigger
    AFTER DELETE
    ON saved_pics_rating
BEGIN
    UPDATE saved_pics
    SET user_rating_sum = user_rating_sum - old.rating,
        rate_user_count = rate_user_count - 1
    WHERE file_uid = old.file_uid;
END;


CREATE TABLE IF NOT EXISTS pic_rate_counter
(
    rate  INTEGER NOT NULL,
    count INTEGER NOT NULL,
    PRIMARY KEY (rate)
) WITHOUT ROWID, STRICT;


CREATE TRIGGER IF NOT EXISTS saved_pics_update_trigger
    AFTER UPDATE
    ON saved_pics
BEGIN
    UPDATE pic_rate_counter
    SET count = count + 1
    WHERE rate = new.user_rate;
    UPDATE pic_rate_counter
    SET count = count - 1
    WHERE rate = old.user_rate;
END;


CREATE TRIGGER IF NOT EXISTS saved_pics_insert_trigger
    AFTER INSERT
    ON saved_pics
BEGIN
    -- 新图片插入时，增加其 user_rate 对应的计数
    INSERT INTO pic_rate_counter (rate, count)
    VALUES (new.user_rate, 1)
    ON CONFLICT (rate) DO UPDATE SET count = count + 1;
END;


CREATE TRIGGER IF NOT EXISTS saved_pics_delete_trigger
    AFTER DELETE
    ON saved_pics
BEGIN
    -- 旧图片删除时，减少其 user_rate 对应的计数
    UPDATE pic_rate_counter
    SET count = count - 1
    WHERE rate = old.user_rate;
END;
