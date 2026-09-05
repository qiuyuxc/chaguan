package db

import (
	"errors"
	"strconv"
	"strings"
)

// 积分内部一律以「分」为单位存整数(1 积分 = 100 分),对外两位小数。
// 绝不用浮点:float 算钱会累积误差,随机拆奖池再求和就对不上账。
// 涉及哪些列见迁移 0024;新增积分字段时记住存的是「分」。
const PointScale = 100

// ErrBadPoints 积分输入不合法(空、带非数字、超过两位小数)。
var ErrBadPoints = errors.New("积分格式不对")

// Pts 把整数积分换成内部单位。写常量时用它,别直接写 500 —— 那样看不出是 5 还是 500。
func Pts(n int64) int64 { return n * PointScale }

// ParsePoints 解析用户输入:"3.24" → 324,"5" → 500,".5" → 50。
// 只认最多两位小数;负号、指数、千分位一律拒掉(范围由调用方判)。
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
		if len(frac) == 1 {
			frac += "0" // ".5" 是 5 角不是 5 分
		}
		v, err := strconv.ParseInt(frac, 10, 64)
		if err != nil || v < 0 {
			return 0, ErrBadPoints
		}
		n += v
	}
	return n, nil
}

// FormatPoints 渲染成人看的字符串:整数不带小数点(500 → "5"),末尾的 0 也去掉
// (324 → "3.24",320 → "3.2"),免得满屏 ".00"。
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
