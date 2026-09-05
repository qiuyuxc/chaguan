package handlers

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, csrf := auth.NewToken(), auth.NewToken()
	if err := s.store.CreateSession(userID, token, csrf, auth.SessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   auth.SessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.Secure(r),
	})
	return nil
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.Secure(r),
	})
	s.store.VacuumSessions()
}

// security 读后台的邮件/人机验证设置;读失败时按「都没开」处理,
// 免得设置表出问题就把登录注册整条路堵死。
func (s *Server) security() db.Security {
	sec, err := s.store.Security()
	if err != nil {
		return db.Security{}
	}
	return sec
}

// ---------- 登录 ----------

type loginData struct {
	web.Base
	Error    string
	Notice   string // 验证成功 / 密码已重置等一次性提示
	Name     string
	Unverify bool // 因邮箱未验证被拒:页面上给「重发验证邮件」入口
}

// twoFAData 两步验证第二步的页面数据。
type twoFAData struct {
	web.Base
	Error string
}

const twoFACookie = "bbs_2fa"

// begin2FA 密码已过但开了两步验证:发一枚 10 分钟的一次性令牌进 cookie,
// 会话要等验证码对上才建立。
func (s *Server) begin2FA(w http.ResponseWriter, r *http.Request, userID int64) error {
	token := auth.NewToken()
	if err := s.store.CreateEmailToken(token, userID, "2fa", "", 10*time.Minute); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     twoFACookie,
		Value:    token,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.Secure(r),
	})
	return nil
}

func (s *Server) clear2FACookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: twoFACookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: auth.Secure(r),
	})
}

// pending2FAUser 取出等待验证码的那个账号。
func (s *Server) pending2FAUser(r *http.Request) (*db.User, string) {
	c, err := r.Cookie(twoFACookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	t, err := s.store.EmailTokenOf(c.Value, "2fa")
	if err != nil || t == nil {
		return nil, ""
	}
	u, err := s.store.GetUserByID(t.UserID)
	if err != nil || u == nil {
		return nil, ""
	}
	return u, t.Token
}

// twoFAForm GET /login/2fa:输入验证器里的 6 位码。
func (s *Server) twoFAForm(w http.ResponseWriter, r *http.Request) {
	if u, _ := s.pending2FAUser(r); u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.rend.Render(w, 200, "login_2fa", twoFAData{Base: s.base(r, "两步验证")})
}

// twoFA POST /login/2fa:验证码通过才发会话。
func (s *Server) twoFA(w http.ResponseWriter, r *http.Request) {
	u, token := s.pending2FAUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !auth.TOTPVerify(u.TOTPSecret.String, r.FormValue("code")) {
		s.rend.Render(w, 200, "login_2fa", twoFAData{
			Base: s.base(r, "两步验证"), Error: "验证码不对,请用验证器里当前的 6 位数字",
		})
		return
	}
	if err := s.store.MarkEmailTokenUsed(token); err != nil {
		s.serverError(w, err)
		return
	}
	s.clear2FACookie(w, r)
	if err := s.setSession(w, r, u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	info := auth.From(r.Context())
	if info.User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	d := loginData{Base: s.base(r, "登录")}
	switch r.URL.Query().Get("ok") {
	case "verified":
		d.Notice = "邮箱验证成功,现在可以登录了"
	case "reset":
		d.Notice = "密码已重置,请用新密码登录"
	}
	s.rend.Render(w, 200, "login", d)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")

	fail := func(msg string, unverified bool) {
		s.rend.Render(w, 200, "login", loginData{
			Base:  s.base(r, "登录"),
			Error: msg, Name: name, Unverify: unverified,
		})
	}

	user, err := s.store.GetUserByLogin(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 用户不存在与密码错误给同一句提示,避免账号枚举
	if user == nil || !auth.CheckPassword(user.PasswordHash, password) {
		fail("账户名/邮箱或密码不正确", false)
		return
	}
	if user.BannedUntil.Valid && user.BannedUntil.Int64 > time.Now().Unix() {
		until := time.Unix(user.BannedUntil.Int64, 0).Format("2006-01-02")
		fail("账号已被封禁,将于 "+until+" 解封", false)
		return
	}
	if user.NeedsEmailVerify() {
		fail("邮箱还没验证,请先点验证邮件里的链接", true)
		return
	}
	if user.TOTPEnabled && user.TOTPSecret.Valid && user.TOTPSecret.String != "" {
		if err := s.begin2FA(w, r, user.ID); err != nil {
			s.serverError(w, err)
			return
		}
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}
	if err := s.setSession(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- 注册 ----------

type registerData struct {
	web.Base
	Error       string
	Name        string
	Email       string
	EmailMode   bool   // 开了邮件注册:必须填邮箱并验证
	CaptchaSite string // 非空则渲染 Turnstile 组件
	Sent        string // 非空表示已发出验证邮件,页面改为提示态
	SendErr     string // 账号已建但邮件没发出去
}

func (s *Server) registerPage(w http.ResponseWriter, r *http.Request, sec db.Security, d registerData) {
	d.Base = s.base(r, "注册")
	d.EmailMode = sec.EmailRegisterOn()
	if sec.CaptchaOn() {
		d.CaptchaSite = sec.TurnstileSite
	}
	s.rend.Render(w, 200, "register", d)
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if auth.From(r.Context()).User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.registerPage(w, r, s.security(), registerData{})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	sec := s.security()
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	fail := func(msg string) {
		s.registerPage(w, r, sec, registerData{Error: msg, Name: name, Email: email})
	}
	if msg := s.checkCaptcha(r, sec); msg != "" {
		fail(msg)
		return
	}
	switch {
	case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
		fail("用户名需要 2–24 个字符")
		return
	case strings.ContainsAny(name, " \t\r\n@/"):
		fail("用户名不能包含空格、@ 或斜杠")
		return
	case len(password) < 8:
		fail("密码至少 8 位")
		return
	}
	emailMode := sec.EmailRegisterOn()
	if emailMode {
		if !validEmail(email) {
			fail("请填写有效的邮箱地址")
			return
		}
		taken, err := s.store.GetUserByEmail(email)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if taken != nil {
			fail("该邮箱已被注册")
			return
		}
	} else {
		email = "" // 没开邮件注册就不收集邮箱
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	userID, err := s.store.CreateUser(name, email, hash)
	if err == db.ErrDuplicateName {
		fail("用户名或邮箱已被占用")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}

	if emailMode {
		// 邮件注册:先不给会话,等对方点了链接再放进来
		if err := s.sendVerifyMail(r, sec, userID, name, email); err != nil {
			s.registerPage(w, r, sec, registerData{
				Sent:    email,
				SendErr: "验证邮件发送失败:" + err.Error(),
			})
			return
		}
		s.registerPage(w, r, sec, registerData{Sent: email})
		return
	}
	if err := s.setSession(w, r, userID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- 邮箱验证 ----------

// verifyEmail GET /verify/email?token=…:点邮件链接完成验证,然后去登录。
func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.EmailTokenOf(token, "verify")
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		s.rend.Render(w, 200, "verify_resend", resendData{
			Base:  s.base(r, "邮箱验证"),
			Error: "链接无效或已过期,可以在下面重新发一封。",
		})
		return
	}
	if err := s.store.ConsumeEmailVerify(t.Token, t.UserID, t.Email); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/login?ok=verified", http.StatusSeeOther)
}

type resendData struct {
	web.Base
	Error       string
	Email       string
	Sent        bool
	CaptchaSite string
}

func (s *Server) resendPage(w http.ResponseWriter, r *http.Request, sec db.Security, d resendData) {
	d.Base = s.base(r, "重发验证邮件")
	if sec.CaptchaOn() {
		d.CaptchaSite = sec.TurnstileSite
	}
	s.rend.Render(w, 200, "verify_resend", d)
}

func (s *Server) resendForm(w http.ResponseWriter, r *http.Request) {
	s.resendPage(w, r, s.security(), resendData{})
}

// resendVerify POST /verify/resend:按邮箱重发验证信。
// 无论邮箱是否存在都回同一句提示,避免被拿来探测注册情况。
func (s *Server) resendVerify(w http.ResponseWriter, r *http.Request) {
	sec := s.security()
	email := strings.TrimSpace(r.FormValue("email"))
	if msg := s.checkCaptcha(r, sec); msg != "" {
		s.resendPage(w, r, sec, resendData{Error: msg, Email: email})
		return
	}
	if !validEmail(email) {
		s.resendPage(w, r, sec, resendData{Error: "请填写有效的邮箱地址", Email: email})
		return
	}
	user, err := s.store.GetUserByEmail(email)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if user != nil && user.NeedsEmailVerify() && sec.MailReady() {
		if err := s.sendVerifyMail(r, sec, user.ID, user.Name, email); err != nil {
			s.resendPage(w, r, sec, resendData{Error: "邮件发送失败:" + err.Error(), Email: email})
			return
		}
	}
	s.resendPage(w, r, sec, resendData{Sent: true, Email: email})
}

// ---------- 忘记密码 ----------

type forgotData struct {
	web.Base
	Error       string
	Email       string
	Sent        bool
	Disabled    bool // 没配 SMTP:不给假希望,直接说明找管理员
	CaptchaSite string
}

func (s *Server) forgotPage(w http.ResponseWriter, r *http.Request, sec db.Security, d forgotData) {
	d.Base = s.base(r, "找回密码")
	d.Disabled = !sec.MailReady()
	if sec.CaptchaOn() {
		d.CaptchaSite = sec.TurnstileSite
	}
	s.rend.Render(w, 200, "forgot", d)
}

func (s *Server) forgotForm(w http.ResponseWriter, r *http.Request) {
	s.forgotPage(w, r, s.security(), forgotData{})
}

func (s *Server) forgot(w http.ResponseWriter, r *http.Request) {
	sec := s.security()
	email := strings.TrimSpace(r.FormValue("email"))
	if !sec.MailReady() {
		s.forgotPage(w, r, sec, forgotData{})
		return
	}
	if msg := s.checkCaptcha(r, sec); msg != "" {
		s.forgotPage(w, r, sec, forgotData{Error: msg, Email: email})
		return
	}
	if !validEmail(email) {
		s.forgotPage(w, r, sec, forgotData{Error: "请填写有效的邮箱地址", Email: email})
		return
	}
	user, err := s.store.GetUserByEmail(email)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 邮箱不存在也照样回「已发送」,不泄露谁注册过
	if user != nil {
		if err := s.sendResetMail(r, sec, user.ID, user.Name, email); err != nil {
			s.forgotPage(w, r, sec, forgotData{Error: "邮件发送失败:" + err.Error(), Email: email})
			return
		}
	}
	s.forgotPage(w, r, sec, forgotData{Sent: true, Email: email})
}

// ---------- 重置密码 ----------

type resetData struct {
	web.Base
	Token string
	Error string
	Bad   bool // 令牌无效/过期
}

func (s *Server) resetForm(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	t, err := s.store.EmailTokenOf(token, "reset")
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		s.rend.Render(w, 200, "reset", resetData{Base: s.base(r, "重置密码"), Bad: true})
		return
	}
	s.rend.Render(w, 200, "reset", resetData{Base: s.base(r, "重置密码"), Token: token})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	t, err := s.store.EmailTokenOf(token, "reset")
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		s.rend.Render(w, 200, "reset", resetData{Base: s.base(r, "重置密码"), Bad: true})
		return
	}
	password := r.FormValue("password")
	confirm := r.FormValue("password2")
	fail := func(msg string) {
		s.rend.Render(w, 200, "reset", resetData{
			Base: s.base(r, "重置密码"), Token: token, Error: msg,
		})
	}
	switch {
	case len(password) < 8:
		fail("密码至少 8 位")
		return
	case password != confirm:
		fail("两次输入的密码不一致")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 重置成功会同时踢掉该账号的全部会话(可能已被他人登录)
	if err := s.store.ConsumeEmailReset(t.Token, t.UserID, hash); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/login?ok=reset", http.StatusSeeOther)
}

// ---------- 登出 ----------

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
