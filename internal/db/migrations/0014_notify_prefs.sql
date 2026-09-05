-- 通知偏好(个人中心 →「设置」):
--   notify_scope 接收范围 all=全部 / reply=仅回复评论 / mention=仅 @提及 / none=不接收
--   notify_freq  接收频率(秒),0 = 实时推送(SSE);其余为轮询间隔,如 300 / 1800
ALTER TABLE users ADD COLUMN notify_scope TEXT NOT NULL DEFAULT 'all';
ALTER TABLE users ADD COLUMN notify_freq INTEGER NOT NULL DEFAULT 0;
