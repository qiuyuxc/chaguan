-- 巡检从 10 分钟一次改成 1 分钟一次(定点开奖的精度就是巡检周期),
-- 顺手给红包超时那条扫描加索引:原来它是全表过滤 kind + rp_status + created_at,
-- 一分钟跑一次的话不该每次都扫全表。
CREATE INDEX idx_dm_messages_rp ON dm_messages(kind, rp_status, created_at);
