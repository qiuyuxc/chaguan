-- 勋章体系:不再由用户自定义文案,改为「发放/兑换 → 佩戴」。
-- users.badge_text 保留为「当前显示的标签」缓存(NULL=跟随身份, ''=隐藏, 文本=佩戴中的勋章名),
-- 这样帖子/列表里既有的 roleBadge 渲染逻辑不用动;badge_id 记的是佩戴来源。
CREATE TABLE badges (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL UNIQUE,
  note       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE TABLE user_badges (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  badge_id   INTEGER NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  source     TEXT NOT NULL DEFAULT 'admin',  -- admin=后台发放 shop=积分兑换 event=活动
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, badge_id)
);
CREATE INDEX idx_user_badges_badge ON user_badges(badge_id);

ALTER TABLE users ADD COLUMN badge_id INTEGER REFERENCES badges(id);

-- 积分商城:商品 + 兑换记录
CREATE TABLE shop_items (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,                 -- badge=勋章 | checkin=签到加成
  name       TEXT NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  price      INTEGER NOT NULL,
  badge_id   INTEGER REFERENCES badges(id) ON DELETE CASCADE, -- kind=badge 时指向勋章
  bonus      INTEGER NOT NULL DEFAULT 0,    -- kind=checkin:每天多给的积分
  days       INTEGER NOT NULL DEFAULT 0,    -- kind=checkin:有效天数(0=不限期)
  stock      INTEGER NOT NULL DEFAULT -1,   -- -1=不限量
  active     INTEGER NOT NULL DEFAULT 1,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE shop_orders (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id    INTEGER NOT NULL,
  name       TEXT NOT NULL,                 -- 下单时的名称快照
  price      INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_shop_orders_user ON shop_orders(user_id, id DESC);

-- 存量的自定义称号迁成正式勋章,并让原主佩戴上,老数据不至于凭空消失
INSERT INTO badges (name, note, created_at)
SELECT DISTINCT badge_text, '由旧版自定义称号迁移', strftime('%s','now')
FROM users WHERE badge_text IS NOT NULL AND badge_text <> '';

INSERT INTO user_badges (user_id, badge_id, source, created_at)
SELECT u.id, b.id, 'event', strftime('%s','now')
FROM users u JOIN badges b ON b.name = u.badge_text
WHERE u.badge_text IS NOT NULL AND u.badge_text <> '';

UPDATE users SET badge_id = (SELECT b.id FROM badges b WHERE b.name = users.badge_text)
WHERE badge_text IS NOT NULL AND badge_text <> '';
