package db

import (
	"errors"
	"strconv"
	"strings"
)

// 积分内部一律以「分」为单位存(1 积分 = 100 分),对外两位小数。
//
// 为什么不用浮点:float 算钱会累积误差 —— 10 积分随机拆三份再加回来就不一定等于
// 10 了,对账立刻对不上。整数放大是所有钱系统的标准做法,拆分、比较、求和全是精确的。
//
// 涉及的列(迁移 0024 一次性 ×100):users.points / users.checkin_bonus /
// point_logs.delta / point_logs.balance / checkins.points / threads.price /
// lotteries.stake+pool+sponsor / lottery_entries.stake+prize /
// shop_items.price+bonus / shop_orders.price / dm_messages.amount。
//
// 以后新增任何积分字段,记住存的是「分」。
const PointScale = 100

// ErrBadPoints 积分输入不合法(空、带非数字、超过两位小数)。
var ErrBadPoints = errors.New("积分格式不对")

// Pts 把整数积分换成内部单位。写常量(签到 +5、改名 3 分)时用它,
// 别在代码里直接写 500 —— 那样一眼看不出是 5 积分还是 500 积分。
func Pts(n int64) int64 { return n * PointScale }

// ParsePoints 解析用户输入的积分:"3.24" → 324,"5" → 500,".5" → 50。
// 只认最多两位小数;负号、指数、千分位一律拒掉(调用方自己判范围)。
func ParsePoints(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrBadPoints
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && !hasDot {
		return 0, ErrBadPoints
	}
	var n int64
	if whole != "" {
		v, err := strconv.ParseInt(whole, 10, 64)
		if err != nil || v < 0 || v > 1<<50 {
			return 0, ErrBadPoints
		}
		n = v * PointScale
	}
	if hasDot {
		if len(frac) == 0 || len(frac) > 2 {
			return 0, ErrBadPoints
		}
		// 一位小数按十分位补齐:".5" 是 5 角不是 5 分
		if len(frac) == 1 {
			frac += "0"
		}
		v, err := strconv.ParseInt(frac, 10, 64)
		if err != nil || v < 0 {
			return 0, ErrBadPoints
		}
		n += v
	}
	return n, nil
}

// FormatPoints 把内部单位渲染成人看的字符串。整数不带小数点(500 → "5"),
// 末尾的 0 也去掉(324 → "3.24",320 → "3.2"),免得满屏 ".00" 很吵。
func FormatPoints(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	whole, frac := v/PointScale, v%PointScale
	s := strconv.FormatInt(whole, 10)
	if frac > 0 {
		f := strconv.FormatInt(frac, 10)
		if len(f) < 2 {
			f = "0" + f
		}
		s += "." + strings.TrimRight(f, "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}
