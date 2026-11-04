-- The per-file rows already carry the album key; keep group metadata on each row.
CREATE TABLE album_files_v7 (
  chat_id INTEGER NOT NULL,
  topic_id INTEGER NOT NULL,
  group_id TEXT NOT NULL,
  msg_id INTEGER NOT NULL,
  file_id TEXT NOT NULL,
  file_unique_id TEXT NOT NULL,
  byte_size INTEGER,
  expires_at INTEGER NOT NULL,
  photo_only INTEGER NOT NULL CHECK(photo_only IN (0,1)),
  PRIMARY KEY(chat_id,topic_id,group_id,msg_id)
) WITHOUT ROWID, STRICT;
INSERT INTO album_files_v7(chat_id,topic_id,group_id,msg_id,file_id,file_unique_id,byte_size,expires_at,photo_only)
SELECT f.chat_id,f.topic_id,f.group_id,f.msg_id,f.file_id,f.file_unique_id,f.byte_size,a.expires_at,a.photo_only
FROM album_files f JOIN album_index a USING(chat_id,topic_id,group_id);
DROP TABLE album_files;
DROP TABLE album_index;
ALTER TABLE album_files_v7 RENAME TO album_files;
CREATE INDEX idx_album_files_message ON album_files(chat_id,topic_id,msg_id);
CREATE INDEX idx_album_files_expiry ON album_files(expires_at);
