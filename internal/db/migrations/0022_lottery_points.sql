-- 抽奖分两类:
--   item   = 实物/自定义奖,平台只负责抽人,奖品楼主自己发
--   points = 积分奖,平台开奖时把奖池自动发给中奖者
-- 故意不动 threads.kind —— `kind = 'lottery'` 的判断散在 gate.go / forum.go /
-- thread_row.html / new_thread.html 好几处,只在 lotteries 上加字段更省事,
-- 存量抽奖帖默认按实物奖处理(行为跟以前一致)。
ALTER TABLE lotteries ADD COLUMN pay_kind TEXT NOT NULL DEFAULT 'item';

-- 楼主自掏进奖池的积分。发帖时就从他账上预扣,不能等开奖再扣 ——
-- 否则中间他把积分花光了,开奖就发不出来。开不了奖(无人参与)要退回。
ALTER TABLE lotteries ADD COLUMN sponsor INTEGER NOT NULL DEFAULT 0;

-- 参与人数上限,先来后到,满了后来的人回复照发但不进参与名单。0 = 不限。
ALTER TABLE lotteries ADD COLUMN max_entries INTEGER NOT NULL DEFAULT 0;

-- 到点自动开奖的时间戳(NULL = 只能楼主/管理员手动开)。
-- 自动开奖复用红包超时退回那个进程内巡检,不新增调度机制。
ALTER TABLE lotteries ADD COLUMN draw_at INTEGER;

-- status 多一个取值:canceled = 无人参与,奖池已退回楼主。
CREATE INDEX idx_lotteries_due ON lotteries(status, draw_at);
