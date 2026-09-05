// 账号设置(头像菜单 → 更多 → 账号):账户名、邮箱绑定、密码、两步验证。
// 显示名不在这里改 —— 那是「编辑资料」的事,而且要花积分。
package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

type accountData struct {
	web.Base
	Me        *db.User
	MailReady bool   // 没配 SMTP 就不给绑定邮箱的入口
	Setup     string // 刚生成、待确认的 2FA 密钥
	SetupURL  string // otpauth:// 链接
	Error     string
	Saved     string
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, d accountData) {
	user := auth.From(r.Context()).User
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	me, err := s.store.GetUserByID(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if me == nil {
		http.NotFound(w, r)
		return
	}
	d.Base = s.base(r, "账号")
	d.Me = me
	d.MailReady = s.security().MailReady()
	s.rend.Render(w, 200, "account", d)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(w, r) == nil {
		return
	}
	s.accountPage(w, r, accountData{Saved: accountNotice(r.URL.Query().Get("ok"))})
}

// accountNotice 把跳转参数翻成提示文案。
func accountNotice(code string) string {
	switch code {
	case "name":
		return "账户名已更新,下次登录用新的账户名"
	case "password":
		return "密码已修改"
	case "mail":
		return "验证邮件已发出,点邮件里的链接完成绑定"
	case "2fa-on":
		return "两步验证已开启"
	case "2fa-off":
		return "两步验证已关闭"
	default:
		return ""
	}
}

// accountName POST /account/name:改登录用的账户名。
func (s *Server) accountName(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	name := strings.TrimSpace(r.FormValue("login_name"))
	switch {
	case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
		s.accountPage(w, r, accountData{Error: "账户名需要 2–24 个字符"})
		return
	case strings.ContainsAny(name, " \t\r\n@/"):
		s.accountPage(w, r, accountData{Error: "账户名不能包含空格、@ 或斜杠"})
		return
	}
	if err := s.store.UpdateLoginName(user.ID, name); err != nil {
		if err == db.ErrDuplicateName {
			s.accountPage(w, r, accountData{Error: "这个账户名已被占用"})
			return
		}
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account?ok=name", http.StatusSeeOther)
}

// accountPassword POST /account/password:改密码(需要旧密码),改完踢掉其他会话。
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	me, err := s.store.GetUserByID(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	old := r.FormValue("old")
	pw := r.FormValue("password")
	pw2 := r.FormValue("password2")
	if !auth.CheckPassword(me.PasswordHash, old) {
		s.accountPage(w, r, accountData{Error: "当前密码不正确"})
		return
	}
	switch {
	case len(pw) < 8:
		s.accountPage(w, r, accountData{Error: "新密码至少 8 位"})
		return
	case pw != pw2:
		s.accountPage(w, r, accountData{Error: "两次输入的新密码不一致"})
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.UpdateUserPassword(user.ID, hash); err != nil {
		s.serverError(w, err)
		return
	}
	// 换密码后其他设备上的登录一并失效,再给当前浏览器重建会话
	if err := s.store.DeleteUserSessions(user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.setSession(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account?ok=password", http.StatusSeeOther)
}

// accountEmail POST /account/email:绑定或更换邮箱,发验证信确认。
func (s *Server) accountEmail(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	sec := s.security()
	if !sec.MailReady() {
		s.accountPage(w, r, accountData{Error: "站点还没配置发信服务,暂时无法绑定邮箱"})
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if !validEmail(email) {
		s.accountPage(w, r, accountData{Error: "请填写有效的邮箱地址"})
		return
	}
	taken, err := s.store.GetUserByEmail(email)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if taken != nil && taken.ID != user.ID {
		s.accountPage(w, r, accountData{Error: "该邮箱已被其他账号使用"})
		return
	}
	if err := s.sendVerifyMail(r, sec, user.ID, user.Name, email); err != nil {
		s.accountPage(w, r, accountData{Error: "邮件发送失败:" + err.Error()})
		return
	}
	http.Redirect(w, r, "/account?ok=mail", http.StatusSeeOther)
}

// account2FASetup POST /account/2fa/setup:生成密钥,页面上显示出来等验证码确认。
func (s *Server) account2FASetup(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	secret := auth.NewTOTPSecret()
	if err := s.store.SetTOTPSecret(user.ID, secret); err != nil {
		s.serverError(w, err)
		return
	}
	s.accountPage(w, r, accountData{
		Setup:    secret,
		SetupURL: auth.OTPAuthURL(s.siteName(), user.Name, secret),
	})
}

// account2FAEnable POST /account/2fa/enable:验证码对上才真正开启。
func (s *Server) account2FAEnable(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	me, err := s.store.GetUserByID(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !me.TOTPSecret.Valid || me.TOTPSecret.String == "" {
		s.accountPage(w, r, accountData{Error: "请先点「生成密钥」"})
		return
	}
	if !auth.TOTPVerify(me.TOTPSecret.String, r.FormValue("code")) {
		s.accountPage(w, r, accountData{
			Error:    "验证码不对,请用验证器里当前的 6 位数字再试一次",
			Setup:    me.TOTPSecret.String,
			SetupURL: auth.OTPAuthURL(s.siteName(), me.Name, me.TOTPSecret.String),
		})
		return
	}
	if err := s.store.EnableTOTP(user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account?ok=2fa-on", http.StatusSeeOther)
}

// account2FADisable POST /account/2fa/disable:关闭需要再验一次码,避免会话被借用就能关。
func (s *Server) account2FADisable(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	me, err := s.store.GetUserByID(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if me.TOTPEnabled && !auth.TOTPVerify(me.TOTPSecret.String, r.FormValue("code")) {
		s.accountPage(w, r, accountData{Error: "验证码不对,无法关闭两步验证"})
		return
	}
	if err := s.store.DisableTOTP(user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account?ok=2fa-off", http.StatusSeeOther)
}
