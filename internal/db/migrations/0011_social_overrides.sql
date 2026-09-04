-- 后台可覆盖展示用社交数据:关注数 / 粉丝数 / 获赞数。
-- NULL = 按真实统计(follows / post_likes 聚合)。
ALTER TABLE users ADD COLUMN stat_following INTEGER;
ALTER TABLE users ADD COLUMN stat_followers INTEGER;
ALTER TABLE users ADD COLUMN stat_liked INTEGER;
