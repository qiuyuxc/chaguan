-- 版主按版块管理:users.role='mod' 的成员在此登记管辖版块。
-- 管理员不在此表内(隐式全站权限)。一个版主可登记多个版块。
CREATE TABLE IF NOT EXISTS category_mods (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, category_id)
);
CREATE INDEX IF NOT EXISTS idx_category_mods_cat ON category_mods(category_id);
