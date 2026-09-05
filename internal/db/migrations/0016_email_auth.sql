-- 邮箱验证与密码找回:
--   users.email_verified_at  邮箱通过验证的时间;NULL 且 email 非空 = 待验证
--   email_tokens             一次性令牌(注册验证 / 找回密码)
-- 说明:老账号 email 为 NULL,不受「邮件注册」开关影响,开关打开也不会把它们锁在门外。
ALTER TABLE users ADD COLUMN email_verified_at INTEGER;

CREATE TABLE email_tokens (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,   -- verify=注册验证 | reset=找回密码
  email      TEXT NOT NULL,   -- 令牌指向的邮箱(验证通过后写回 users.email)
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER
);
CREATE INDEX idx_email_tokens_user ON email_tokens(user_id, kind);
CREATE INDEX idx_email_tokens_expires ON email_tokens(expires_at);
