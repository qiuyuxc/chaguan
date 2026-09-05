// TOTP(RFC 6238)两步验证:HMAC-SHA1、6 位、30 秒步长,用标准库实现,不引第三方。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpDigits = 6
	totpStep   = 30 * time.Second
	// 允许前后各一个时间窗,容忍手机与服务器的时钟偏差
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret 生成一枚 base32 密钥(20 字节,与主流验证器兼容)。
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 系统随机源不可用属于致命错误
	}
	return b32.EncodeToString(b)
}

// totpAt 算某个时间窗的验证码。
func totpAt(secret string, counter uint64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, code%mod), nil
}

// TOTPCode 当前时间窗的验证码(自测与冒烟用)。
func TOTPCode(secret string) string {
	code, err := totpAt(secret, uint64(time.Now().Unix())/uint64(totpStep.Seconds()))
	if err != nil {
		return ""
	}
	return code
}

// TOTPVerify 校验验证码,容忍前后一个时间窗。
func TOTPVerify(secret, code string) bool {
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != totpDigits {
		return false
	}
	counter := uint64(time.Now().Unix()) / uint64(totpStep.Seconds())
	for d := -totpSkew; d <= totpSkew; d++ {
		c := counter
		if d < 0 {
			if c < uint64(-d) {
				continue
			}
			c -= uint64(-d)
		} else {
			c += uint64(d)
		}
		want, err := totpAt(secret, c)
		if err != nil {
			return false
		}
		// 定长比较,避免早退带来的时间差
		if hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

// OTPAuthURL 生成验证器可识别的 otpauth:// 链接(手动添加或生成二维码用)。
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{
		"secret": {secret},
		"issuer": {issuer},
		"digits": {fmt.Sprint(totpDigits)},
		"period": {fmt.Sprint(int(totpStep.Seconds()))},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}
