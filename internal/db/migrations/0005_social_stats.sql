-- 关注关系与帖子点赞:支撑个人主页 关注/粉丝/获赞 三区块统计。
-- 本轮只提供统计与展示;关注/点赞交互在后续版本接入写入口。

CREATE TABLE follows (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  follower_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  followee_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at  INTEGER NOT NULL,
  UNIQUE(follower_id, followee_id)
);
CREATE INDEX idx_follows_follower ON follows(follower_id, followee_id);
CREATE INDEX idx_follows_followee ON follows(followee_id, follower_id);

CREATE TABLE post_likes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  post_id    INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  UNIQUE(post_id, user_id)
);
CREATE INDEX idx_post_likes_post ON post_likes(post_id, user_id);
CREATE INDEX idx_post_likes_user ON post_likes(user_id, post_id);
