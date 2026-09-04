-- 社交覆盖改为「起点基准」:设置值之后,真实新增继续累计显示。
-- 记录设置时刻的真实统计作基准,显示 = 设置值 + (当前真实 - 基准)(只增不减)。
ALTER TABLE users ADD COLUMN stat_following_base INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN stat_followers_base INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN stat_liked_base INTEGER NOT NULL DEFAULT 0;

-- 旧覆盖记录(0011 阶段为固定显示值)迁移:以当时真实统计为基准,保持展示值不变。
UPDATE users SET stat_following_base = (SELECT COUNT(*) FROM follows WHERE follower_id = users.id) WHERE stat_following IS NOT NULL;
UPDATE users SET stat_followers_base = (SELECT COUNT(*) FROM follows WHERE followee_id = users.id) WHERE stat_followers IS NOT NULL;
UPDATE users SET stat_liked_base = (SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id WHERE p.author_id = users.id) WHERE stat_liked IS NOT NULL;
