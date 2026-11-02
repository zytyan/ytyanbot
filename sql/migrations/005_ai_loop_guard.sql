-- Only explicitly triggered session submissions are counted, never ordinary chat history.
CREATE TABLE ai_session_request_origins (
  id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES ai_sessions(id),
  chat_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL,
  bot_streak INTEGER NOT NULL CHECK(bot_streak BETWEEN 0 AND 3),
  suppressed INTEGER NOT NULL CHECK(suppressed IN (0,1)),
  UNIQUE(session_id,chat_id,msg_id),
  FOREIGN KEY(chat_id,msg_id) REFERENCES ai_messages(chat_id,msg_id)
);
CREATE INDEX idx_ai_session_request_origins_latest ON ai_session_request_origins(session_id,id DESC);
CREATE INDEX idx_ai_session_request_origins_suppressed ON ai_session_request_origins(chat_id,msg_id) WHERE suppressed=1;
