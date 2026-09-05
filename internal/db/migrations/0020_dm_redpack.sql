-- 私信红包:消息多一种类型,积分在发出时就从发送者账上扣走,
-- 领取时进对方账户,未领取可由发送者撤回退款。
--   kind      text=普通消息 | redpack=红包
--   rp_status 红包状态 open=待领取 | claimed=已领取 | refunded=已退回
ALTER TABLE dm_messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'text';
ALTER TABLE dm_messages ADD COLUMN amount INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dm_messages ADD COLUMN rp_status TEXT NOT NULL DEFAULT '';
ALTER TABLE dm_messages ADD COLUMN rp_at INTEGER;
