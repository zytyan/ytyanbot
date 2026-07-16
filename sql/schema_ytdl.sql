-- encoding: utf-8

CREATE TABLE IF NOT EXISTS yt_dl_results
(
    url          TEXT     NOT NULL,
    audio_only   INT_BOOL NOT NULL CHECK (audio_only IN (0, 1)),
    resolution   INTEGER  NOT NULL,
    file_id      TEXT     NOT NULL, -- 其实有可能为NULL，但是golang的NULL很不爽，所以改用了空字符串作为NULL
    title        TEXT     NOT NULL,
    description  TEXT     NOT NULL,
    uploader     TEXT     NOT NULL,
    upload_count INTEGER  NOT NULL DEFAULT 0,
    PRIMARY KEY (url, audio_only, resolution)
) WITHOUT ROWID;

CREATE INDEX idx_yt_dl_results_file_id ON yt_dl_results(file_id);
