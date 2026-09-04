-- 初始 schema。时间戳一律用 unix 秒 (INTEGER)。
-- 迁移只做加列/加表,不写破坏性变更,保证旧二进制可挂新库启动。

CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
  email         TEXT UNIQUE,
  password_hash TEXT NOT NULL,
  avatar_path   TEXT,
  role          TEXT NOT NULL DEFAULT 'user',   -- user | mod | admin
  bio           TEXT,
  created_at    INTEGER NOT NULL,
  banned_until  INTEGER
);

CREATE TABLE sessions (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE categories (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT,
  position    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE threads (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  category_id  INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  author_id    INTEGER NOT NULL REFERENCES users(id),
  title        TEXT NOT NULL,
  is_pinned    INTEGER NOT NULL DEFAULT 0,
  is_locked    INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  last_post_at INTEGER NOT NULL,
  view_count   INTEGER NOT NULL DEFAULT 0,
  post_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_threads_cat ON threads(category_id, is_pinned DESC, last_post_at DESC);

CREATE TABLE posts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id    INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  author_id    INTEGER NOT NULL REFERENCES users(id),
  content_md   TEXT NOT NULL,
  content_html TEXT NOT NULL,
  is_first     INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  edited_at    INTEGER
);
CREATE INDEX idx_posts_thread ON posts(thread_id, id);

-- Phase 2/3 使用,提前建表避免后续迁移
CREATE TABLE uploads (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id),
  path       TEXT NOT NULL,
  mime       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE notifications (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  actor_id   INTEGER NOT NULL REFERENCES users(id),
  type       TEXT NOT NULL,          -- reply | quote | mention
  thread_id  INTEGER,
  post_id    INTEGER,
  read_at    INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_notifications_user ON notifications(user_id, read_at);
