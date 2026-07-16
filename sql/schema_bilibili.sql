-- encoding: utf-8
CREATE TABLE IF NOT EXISTS bili_inline_results
(
    uid     INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    text    TEXT    NOT NULL DEFAULT '',
    chat_id INTEGER NOT NULL DEFAULT 0,
    msg_id  INTEGER NOT NULL DEFAULT 0,
    created_at INT_UNIX_SEC NOT NULL
);

CREATE INDEX idx_bili_inline_results_created_at ON bili_inline_results(created_at);
