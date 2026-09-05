// Cloudflare Turnstile 人机验证:注册与找回密码时校验前端令牌。
// 只在后台开了开关且两个密钥都填了的时候生效(db.Security.CaptchaOn)。
package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bbs/internal/db"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

var turnstileClient = &http.Client{Timeout: 10 * time.Second}

// checkCaptcha 校验请求里的 Turnstile 令牌。未启用时直接通过。
// 返回空串表示通过,否则是给用户看的错误文案。
func (s *Server) checkCaptcha(r *http.Request, sec db.Security) string {
	if !sec.CaptchaOn() {
		return ""
	}
	token := strings.TrimSpace(r.FormValue("cf-turnstile-response"))
	if token == "" {
		return "请先完成人机验证"
	}
	form := url.Values{
		"secret":   {sec.TurnstileSecret},
		"response": {token},
	}
	if ip := clientIP(r); ip != "" {
		form.Set("remoteip", ip)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "人机验证服务暂时不可用,请稍后再试"
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := turnstileClient.Do(req)
	if err != nil {
		return "无法连接人机验证服务,请稍后再试"
	}
	defer resp.Body.Close()
	var out struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "人机验证返回异常,请稍后再试"
	}
	if !out.Success {
		return "人机验证未通过,请重试"
	}
	return ""
}

// clientIP 取访客 IP:优先反代头,回退 RemoteAddr。
func clientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
