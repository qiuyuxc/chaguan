-- 积分改成支持两位小数:内部一律以「分」为单位存(1 积分 = 100 分)。
-- 绝不用浮点 —— float 算钱会累积误差,随机拆奖池再求和就对不上账了。
-- 这里把所有存量数值一次性 ×100,之后代码里读写的都是「分」。
-- 展示走 web 层的 pts 助手(整数不带小数点),输入走 db.ParsePoints。
UPDATE users        SET points = points * 100, checkin_bonus = checkin_bonus * 100;
UPDATE point_logs   SET delta = delta * 100, balance = balance * 100;
UPDATE checkins     SET points = points * 100;
UPDATE threads      SET price = price * 100;
UPDATE lotteries    SET stake = stake * 100, pool = pool * 100, sponsor = sponsor * 100;
UPDATE lottery_entries SET stake = stake * 100, prize = prize * 100;
UPDATE shop_items   SET price = price * 100, bonus = bonus * 100;
UPDATE shop_orders  SET price = price * 100;
UPDATE dm_messages  SET amount = amount * 100;
