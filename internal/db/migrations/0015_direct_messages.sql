-- 私信:一对一会话 + 消息。
-- 会话用「较小 id, 较大 id」存一行并加唯一约束,避免 A→B 与 B→A 建出两条会话。
-- 正文按纯文本存(前端 pre-wrap 展示),不走 Markdown,聊天场景更直观也更安全。
CREATE TABLE dm_threads (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_a     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- min(两个 id)
  user_b     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- max(两个 id)
  created_at INTEGER NOT NULL,
  last_at    INTEGER NOT NULL,  -- 最后一条消息时间,会话列表按它倒序
  UNIQUE(user_a, user_b)
);
CREATE INDEX idx_dm_threads_a ON dm_threads(user_a, last_at DESC);
CREATE INDEX idx_dm_threads_b ON dm_threads(user_b, last_at DESC);

CREATE TABLE dm_messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id  INTEGER NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
  sender_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  read_at    INTEGER            -- 接收方读到的时间;NULL=未读
);
CREATE INDEX idx_dm_messages_thread ON dm_messages(thread_id, id);
CREATE INDEX idx_dm_messages_unread ON dm_messages(thread_id, read_at);

-- 私信免打扰:1=新私信实时提醒并显示顶栏角标,0=只在私信列表里显示未读
ALTER TABLE users ADD COLUMN notify_dm INTEGER NOT NULL DEFAULT 1;
