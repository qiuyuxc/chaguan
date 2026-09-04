-- 站点级键值设置(当前:站点公告 announcement)。
CREATE TABLE IF NOT EXISTS site_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
