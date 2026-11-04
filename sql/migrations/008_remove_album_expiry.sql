-- Album file indexes no longer expire, so do not persist a redundant expiry timestamp.
CREATE TABLE album_files_v8 (
  chat_id INTEGER NOT NULL,
  topic_id INTEGER NOT NULL,
  group_id TEXT NOT NULL,
  msg_id INTEGER NOT NULL,
  file_id TEXT NOT NULL,
  file_unique_id TEXT NOT NULL,
  byte_size INTEGER,
  photo_only INTEGER NOT NULL CHECK(photo_only IN (0,1)),
  PRIMARY KEY(chat_id,topic_id,group_id,msg_id)
) WITHOUT ROWID, STRICT;
INSERT INTO album_files_v8(chat_id,topic_id,group_id,msg_id,file_id,file_unique_id,byte_size,photo_only)
SELECT chat_id,topic_id,group_id,msg_id,file_id,file_unique_id,byte_size,photo_only FROM album_files;
DROP TABLE album_files;
ALTER TABLE album_files_v8 RENAME TO album_files;
CREATE INDEX idx_album_files_message ON album_files(chat_id,topic_id,msg_id);
