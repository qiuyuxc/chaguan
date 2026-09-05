// 发信辅助:把后台「邮件」设置拼成 mail.Config,并组装验证/找回邮件正文。
package handlers

import (
	"net/http"
	"strings"
	"time"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/internal/mail"
)

const (
	verifyTokenTTL = 24 * time.Hour
	resetTokenTTL  = 2 * time.Hour
)

// absURL 拼出可放进邮件的绝对地址(自建站按请求里的 Host 判断即可)。
func absURL(r *http.Request, path string) string {
	scheme := "http"
	if auth.Secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

// sendMail 按当前设置发一封信;顺手清理过期令牌。
func (s *Server) sendMail(sec db.Security, to, subject, body string) error {
	err := mail.Send(mail.Config{
		Host:   sec.SMTPHost,
		Port:   sec.SMTPPort,
		User:   sec.SMTPUser,
		Pass:   sec.SMTPPass,
		From:   sec.SMTPFrom,
		Secure: sec.SMTPSecure,
	}, to, subject, body)
	s.store.VacuumEmailTokens()
	return err
}

// siteName 邮件正文里用的站点名(读不到就用默认)。
func (s *Server) siteName() string {
	if site, err := s.store.Site(); err == nil {
		return site.Name
	}
	return db.SiteDefaultName
}

// sendVerifyMail 生成验证令牌并把激活链接发给对方。
func (s *Server) sendVerifyMail(r *http.Request, sec db.Security, userID int64, name, email string) error {
	token := auth.NewToken()
	if err := s.store.CreateEmailToken(token, userID, "verify", email, verifyTokenTTL); err != nil {
		return err
	}
	site := s.siteName()
	link := absURL(r, "/verify/email?token="+token)
	body := strings.Join([]string{
		name + ",你好:",
		"",
		"请点击下面的链接完成 " + site + " 的邮箱验证,链接 24 小时内有效。",
		"",
		link,
		"",
		"如果这不是你本人的操作,忽略这封邮件即可。",
		"",
		"—— " + site,
	}, "\n")
	return s.sendMail(sec, email, "验证你的 "+site+" 邮箱", body)
}

// sendResetMail 生成找回令牌并把重置链接发给对方。
func (s *Server) sendResetMail(r *http.Request, sec db.Security, userID int64, name, email string) error {
	token := auth.NewToken()
	if err := s.store.CreateEmailToken(token, userID, "reset", email, resetTokenTTL); err != nil {
		return err
	}
	site := s.siteName()
	link := absURL(r, "/reset?token="+token)
	body := strings.Join([]string{
		name + ",你好:",
		"",
		"我们收到了重置 " + site + " 密码的请求。点击下面的链接设置新密码,链接 2 小时内有效,只能使用一次。",
		"",
		link,
		"",
		"如果不是你发起的,忽略这封邮件即可,你的密码不会变化。",
		"",
		"—— " + site,
	}, "\n")
	return s.sendMail(sec, email, "重置你的 "+site+" 密码", body)
}

// validEmail 只做够用的形式校验:一个 @、无空白、长度合理。
func validEmail(v string) bool {
	if len(v) < 5 || len(v) > 120 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(v, '@')
	if at < 1 || at != strings.LastIndexByte(v, '@') || at == len(v)-1 {
		return false
	}
	return strings.Contains(v[at+1:], ".")
}
