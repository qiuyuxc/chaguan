-- 抽奖分两类:item = 实物/自定义奖(平台只抽人,奖品楼主自己发)、
--             points = 积分奖(开奖时把奖池自动发给中奖者)。
-- 故意不动 threads.kind:`kind = 'lottery'` 的判断散在好几个文件里,
-- 只在 lotteries 上加字段更省事,存量抽奖帖默认按实物奖处理。
ALTER TABLE lotteries ADD COLUMN pay_kind TEXT NOT NULL DEFAULT 'item';

-- 楼主自掏进奖池的积分,发帖时就预扣(不然中途花光了开奖发不出来),开不了奖要退回
ALTER TABLE lotteries ADD COLUMN sponsor INTEGER NOT NULL DEFAULT 0;

-- 参与人数上限,先来后到,满了之后的回复照发但不进名单。0 = 不限
ALTER TABLE lotteries ADD COLUMN max_entries INTEGER NOT NULL DEFAULT 0;

-- 到点自动开奖的时间戳(NULL = 只能手动开),由进程内巡检触发
ALTER TABLE lotteries ADD COLUMN draw_at INTEGER;

-- status 多一个取值:canceled = 无人参与,奖池已退回楼主
CREATE INDEX idx_lotteries_due ON lotteries(status, draw_at);
