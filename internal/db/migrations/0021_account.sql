-- 账户名(登录用)与显示名分离:
--   users.name       继续作为「显示名」(帖子、列表、@提及都用它,展示层查询不用改)
--   users.login_name 新增,专门用于登录;迁移时从 name 复制,老的登录方式照旧可用
-- 登录同时接受账户名或已验证邮箱。
ALTER TABLE users ADD COLUMN login_name TEXT;
UPDATE users SET login_name = name;
CREATE UNIQUE INDEX idx_users_login ON users(login_name COLLATE NOCASE)
  WHERE login_name IS NOT NULL;

-- 两步验证(TOTP):secret 生成后先存下但不启用,验证码校验通过才置 enabled
ALTER TABLE users ADD COLUMN totp_secret TEXT;
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
