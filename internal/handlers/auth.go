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

// ---------- 登录 ----------

type loginData struct {
	web.Base
	Error string
	Name  string
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	info := auth.From(r.Context())
	if info.User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.rend.Render(w, 200, "login", loginData{Base: s.base(r, "登录")})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")

	fail := func(msg string) {
		s.rend.Render(w, 200, "login", loginData{
			Base:  s.base(r, "登录"),
			Error: msg, Name: name,
		})
	}

	user, err := s.store.GetUserByName(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 用户不存在与密码错误给同一句提示,避免用户名枚举
	if user == nil || !auth.CheckPassword(user.PasswordHash, password) {
		fail("用户名或密码不正确")
		return
	}
	if user.BannedUntil.Valid && user.BannedUntil.Int64 > time.Now().Unix() {
		until := time.Unix(user.BannedUntil.Int64, 0).Format("2006-01-02")
		fail("账号已被封禁,将于 " + until + " 解封")
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
	Error string
	Name  string
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if auth.From(r.Context()).User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.rend.Render(w, 200, "register", registerData{Base: s.base(r, "注册")})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")

	fail := func(msg string) {
		s.rend.Render(w, 200, "register", registerData{
			Base:  s.base(r, "注册"),
			Error: msg, Name: name,
		})
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

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	userID, err := s.store.CreateUser(name, "", hash)
	if err == db.ErrDuplicateName {
		fail("用户名已被占用")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.setSession(w, r, userID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- 登出 ----------

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
