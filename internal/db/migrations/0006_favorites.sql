-- 主题收藏:支撑个人主页「收藏」分区与帖子页星标按钮。
CREATE TABLE favorites (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id  INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  UNIQUE(thread_id, user_id)
);
CREATE INDEX idx_favorites_user ON favorites(user_id, thread_id);
CREATE INDEX idx_favorites_thread ON favorites(thread_id, user_id);
