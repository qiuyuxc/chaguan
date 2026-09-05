-- 帖子类型与阅读门槛(由发帖人自己设):
--   kind      normal=普通帖  lottery=抽奖帖
--   min_level 观看需达到的等级(0=不限)
--   price     观看需支付的积分(0=免费),支付一次永久解锁
ALTER TABLE threads ADD COLUMN kind TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE threads ADD COLUMN min_level INTEGER NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN price INTEGER NOT NULL DEFAULT 0;

CREATE TABLE thread_unlocks (
  thread_id  INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  paid       INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (thread_id, user_id)
);

-- 抽奖帖:一个主题一场抽奖,回复即参与(stake>0 时回复要同时投入积分)
CREATE TABLE lotteries (
  thread_id  INTEGER PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
  prize      TEXT NOT NULL,
  winners    INTEGER NOT NULL DEFAULT 1,
  stake      INTEGER NOT NULL DEFAULT 0,   -- 参与需投入的积分(0=只要回复)
  pool       INTEGER NOT NULL DEFAULT 0,   -- 已归集的积分,开奖时分给中奖者
  status     TEXT NOT NULL DEFAULT 'open', -- open | drawn
  drawn_at   INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE lottery_entries (
  thread_id  INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  stake      INTEGER NOT NULL DEFAULT 0,
  won        INTEGER NOT NULL DEFAULT 0,
  prize      INTEGER NOT NULL DEFAULT 0,   -- 中奖分到的积分
  created_at INTEGER NOT NULL,
  PRIMARY KEY (thread_id, user_id)
);
CREATE INDEX idx_lottery_entries_thread ON lottery_entries(thread_id, won);
