-- 积分体系底座:余额挂在 users 上(查询快),每笔变动都进 point_logs(可对账)。
-- 经验从「发帖/回复/获赞」算出来,签到与活动送的经验单独存在 exp_extra 里累加。
ALTER TABLE users ADD COLUMN points INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN exp_extra INTEGER NOT NULL DEFAULT 0;
-- 增值服务(商城):签到额外积分与到期时间(bonus_until 为 NULL 表示不限期)
ALTER TABLE users ADD COLUMN checkin_bonus INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN bonus_until INTEGER;

CREATE TABLE point_logs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  delta      INTEGER NOT NULL,          -- 正=收入,负=支出
  balance    INTEGER NOT NULL,          -- 变动后的余额,便于对账
  kind       TEXT NOT NULL,             -- checkin|tip_out|tip_in|admin|unlock_out|unlock_in|lottery_stake|lottery_win|shop
  note       TEXT NOT NULL DEFAULT '',
  thread_id  INTEGER,                   -- 关联主题(打赏/解锁/抽奖)
  peer_id    INTEGER,                   -- 关联的另一方
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_point_logs_user ON point_logs(user_id, id DESC);
CREATE INDEX idx_point_logs_thread ON point_logs(thread_id, kind);

-- 签到:一天一条,day 用服务器本地日期,主键天然防重复
CREATE TABLE checkins (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  day        TEXT NOT NULL,             -- YYYY-MM-DD
  points     INTEGER NOT NULL,
  exp        INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, day)
);
