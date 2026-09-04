-- 用户称号标签:badge_text 三态
--   NULL            → 跟随身份显示(管理员/版主)
--   ''              → 隐藏称号标签
--   非空文本        → 自定义称号,替换身份标签文案
ALTER TABLE users ADD COLUMN badge_text TEXT;
