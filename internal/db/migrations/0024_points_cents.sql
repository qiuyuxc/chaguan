-- 积分改成支持两位小数:内部以「分」为单位存(1 积分 = 100 分),存量数值一次性 ×100。
-- 展示走模板助手 pts,输入走 db.ParsePoints,细节见 internal/db/points_unit.go。
UPDATE users        SET points = points * 100, checkin_bonus = checkin_bonus * 100;
UPDATE point_logs   SET delta = delta * 100, balance = balance * 100;
UPDATE checkins     SET points = points * 100;
UPDATE threads      SET price = price * 100;
UPDATE lotteries    SET stake = stake * 100, pool = pool * 100, sponsor = sponsor * 100;
UPDATE lottery_entries SET stake = stake * 100, prize = prize * 100;
UPDATE shop_items   SET price = price * 100, bonus = bonus * 100;
UPDATE shop_orders  SET price = price * 100;
UPDATE dm_messages  SET amount = amount * 100;
