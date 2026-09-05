// Package auth 提供密码哈希、会话 token 与请求上下文里的认证信息。
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"chaguan/internal/db"
)

const (
	CookieName    = "chaguan_session" // 会话 cookie
	CSRFCookie    = "chaguan_csrf"    // 匿名用户的 CSRF 双提交 cookie
	SessionTTL    = 30 * 24 * time.Hour
	SessionMaxAge = 30 * 24 * 3600
)

// AuthInfo 每个请求的认证状态。未登录时 User 为 nil,
// CSRF 来自匿名 cookie(登录/注册表单用)。
type AuthInfo struct {
	User *db.User
	CSRF string
}

type ctxKey struct{}

func WithAuth(ctx context.Context, a AuthInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

func From(ctx context.Context) AuthInfo {
	a, _ := ctx.Value(ctxKey{}).(AuthInfo)
	return a
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewToken 返回 32 字节随机数的 hex,用作会话/CSRF token。
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 系统随机源不可用属于致命错误
	}
	return hex.EncodeToString(b)
}

// Secure 判断请求是否走 HTTPS(直接 TLS 或反代头),决定 cookie Secure 位。
func Secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
